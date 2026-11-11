package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// errMoved is the cause of stopping a run because a human moved the card.
var errMoved = errors.New("task moved by a human")

// lockWorkspace takes the lock of workspace_root for the life of the process; the OS drops it on a crash.
func lockWorkspace(root string) (*os.File, error) {
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(root, ".tiny-symphony.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // fixed name
	if err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("another tiny-symphony holds %s: %w", root, err)
	}
	return f, nil
}

type active struct {
	status string
	stop   context.CancelCauseFunc
}

type outcome struct {
	id  string
	err error
}

// sched owns the running runs: only the goroutine that calls tick and finish touches the map.
type sched struct {
	cfg                  *Config
	configDir            string
	bl                   *Backlog
	log                  *slog.Logger
	poll, timeout, grace time.Duration
	running              map[string]*active
	done                 chan outcome
	failed               int
	tries                map[string]*tries
	backoff              []time.Duration
	trackerPause         time.Duration
	stopping             bool
}

func newSched(cfg *Config, configDir string, bl *Backlog, log *slog.Logger) *sched {
	return &sched{
		cfg: cfg, configDir: configDir, bl: bl, log: log,
		poll:    time.Duration(cfg.PollSeconds) * time.Second,
		timeout: time.Duration(cfg.RunTimeoutMinutes) * time.Minute,
		grace:   10 * time.Second, trackerPause: 2 * time.Second,
		running: map[string]*active{}, done: make(chan outcome),
		tries: map[string]*tries{}, backoff: []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute},
	}
}

// loop ticks every poll (once: a single tick, then waits for its runs); returns the number of failed runs.
func (s *sched) loop(ctx context.Context, once bool) int {
	s.tick(ctx)
	var next <-chan time.Time
	if !once {
		t := time.NewTicker(s.poll)
		defer t.Stop()
		next = t.C
	}
	for once && len(s.running) > 0 || !once {
		select {
		case o := <-s.done:
			s.stopping = s.stopping || ctx.Err() != nil
			s.finish(ctx, o)
		case <-next:
			s.tick(ctx)
		case <-ctx.Done():
			s.stopping = true
			for len(s.running) > 0 {
				s.finish(ctx, <-s.done)
			}
			return s.failed
		}
	}
	return s.failed
}

// tick is reconcile, collect, dispatch. A tracker read error skips it and leaves the runs alone.
func (s *sched) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tasks, err := s.bl.List(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.log.Error("tracker read failed, tick skipped", "err", err)
		s.failed++
		return
	}
	s.housekeeping(ctx)
	listed := map[string]Task{}
	for _, t := range tasks {
		listed[t.ID] = t
	}
	for id, r := range s.running {
		if t, ok := listed[id]; !ok || norm(t.Status) != norm(r.status) {
			s.log.Info("stopping run, task moved", "task", id)
			r.stop(errMoved)
		}
	}
	for _, t := range tasks {
		if len(s.running) >= s.cfg.MaxAgents {
			return
		}
		if _, busy := s.running[t.ID]; busy {
			continue
		}
		l := s.log.With("task", t.ID, "status", t.Status)
		agent, ok := s.cfg.Agents[t.agentName()]
		proj, known := s.cfg.Projects[norm(t.Project)]
		if !ok || !known {
			l.Info("skipped", "agent", t.agentName(), "project", t.Project, "reason", "agent or project not in config")
			continue
		}
		attempt, lastErr, wait := s.attempt(ctx, t)
		if wait {
			continue
		}
		stop, cancel := context.WithCancelCause(ctx)
		s.running[t.ID] = &active{status: t.Status, stop: cancel}
		go func() {
			s.done <- outcome{t.ID, s.runTask(ctx, stop, t, agent, proj, attempt, lastErr, l)}
		}()
	}
}

func (s *sched) finish(ctx context.Context, o outcome) {
	status := s.running[o.id].status
	s.running[o.id].stop(nil)
	delete(s.running, o.id)
	switch {
	case o.err == nil:
		s.clearTries(o.id)
	case errors.Is(o.err, errMoved):
		s.log.Info("run stopped", "task", o.id, "outcome", "moved", "reason", "status changed, no attempt counted")
	case s.stopping:
		s.log.Info("run stopped", "task", o.id, "outcome", "shutdown")
	case errors.Is(o.err, errTracker):
		s.log.Error("tracker failed, no attempt counted", "task", o.id, "outcome", "tracker", "err", o.err)
		s.failed++
	default:
		s.log.Error("run failed", "task", o.id, "outcome", "failed", "err", o.err)
		s.failed++
		if s.failure(o.id, status, o.err) {
			s.block(ctx, o.id)
		}
	}
}
