package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	agent, actor string
	taken        bool // the claim of actor was seen
	stop         context.CancelCauseFunc
}

type outcome struct {
	id   string
	snap TrackerTask // the task as the run ended: the guard of the daemon's writes
	err  error
}

// sched owns the running runs: only the goroutine that calls tick and finish touches the map.
type sched struct {
	cfg                  *Config
	tr                   Tracker
	log                  *slog.Logger
	poll, timeout, grace time.Duration
	running              map[string]*active
	done                 chan outcome
	failed               int
	tries                map[string]*tries
	max                  map[string]int // max_attempts of the project, by task id
	backoff              []time.Duration
	trackerPause         time.Duration
	stopping             bool
}

func newSched(cfg *Config, tr Tracker, log *slog.Logger) *sched {
	return &sched{
		cfg: cfg, tr: tr, log: log,
		poll:    time.Duration(cfg.PollSeconds) * time.Second,
		timeout: time.Duration(cfg.RunTimeoutMinutes) * time.Minute,
		grace:   10 * time.Second, trackerPause: 2 * time.Second,
		running: map[string]*active{}, done: make(chan outcome),
		tries: map[string]*tries{}, max: map[string]int{}, backoff: []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute},
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

func (s *sched) watching(status string) bool { return slices.Contains(s.cfg.Watch, norm(status)) }

// agentFor is the configured agent named by the task's first usable agent:<name> tag.
func (s *sched) agentFor(t TrackerTask) (string, Agent, bool) {
	for _, tag := range t.Tags {
		if n, ok := strings.CutPrefix(norm(tag), "agent:"); ok {
			if a, known := s.cfg.Agents[n]; known {
				return n, a, true
			}
		}
	}
	return "", Agent{}, false
}

// live reports whether the task has a claim that has not expired.
func live(t TrackerTask) bool {
	if t.Claim == "" {
		return false
	}
	until, err := time.ParseInLocation("2006-01-02T15:04", t.ClaimUntil, time.Local)
	return err != nil || until.After(time.Now())
}

// ready is true when every task in the After: lines of t is closed.
func (s *sched) ready(ctx context.Context, t TrackerTask) bool {
	full, err := s.tr.Get(ctx, t.ID)
	for i := 0; err == nil && i < len(full.After); i++ {
		var dep TrackerTask
		if dep, err = s.tr.Get(ctx, full.After[i]); err == nil && norm(dep.Status) != "closed" {
			return false
		}
	}
	if err != nil {
		s.log.Warn("dependencies not read", "task", t.ID, "err", err)
	}
	return err == nil
}

// reconcile stops a run whose task lost its agent tag, or which our actor had taken and which is now
// back in a watched status or held by someone else. Other moves are the agent's own handover.
func (s *sched) reconcile(ctx context.Context) {
	for id, r := range s.running {
		t, err := s.tr.Get(ctx, id)
		if err != nil {
			continue
		}
		r.taken = r.taken || t.Claim == r.actor
		if !slices.ContainsFunc(t.Tags, func(g string) bool { return norm(g) == "agent:"+r.agent }) || r.taken && (s.watching(t.Status) || live(t) && t.Claim != r.actor) {
			s.log.Info("stopping run, task changed hands", "task", id, "status", t.Status, "claim", t.Claim)
			r.stop(errMoved)
		}
	}
}

// tick is reconcile, collect, dispatch. A tracker read error skips it and leaves the runs alone.
func (s *sched) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	tasks, err := s.tr.List(ctx, s.cfg.Watch)
	var repos map[string]string
	if err == nil {
		repos, err = projectRepos(ctx, s.cfg, s.tr)
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.log.Error("tracker read failed, tick skipped", "err", err)
		s.failed++
		return
	}
	s.housekeeping()
	s.reconcile(ctx)
	s.recoverOrphans(ctx, repos)
	if s.cfg.WatchPR.Enabled {
		s.watchPRs(ctx)
	}
	if s.cfg.Cleanup.Enabled {
		s.cleanup(ctx)
	}
	slices.SortFunc(tasks, func(a, b TrackerTask) int {
		return cmp.Or(cmp.Compare(a.Rank, b.Rank), a.Created.Compare(b.Created), cmp.Compare(idNumber(a.ID), idNumber(b.ID)))
	})
	for _, t := range tasks {
		if len(s.running) >= s.cfg.MaxAgents {
			return
		}
		name, agent, ok := s.agentFor(t)
		repo := repos[t.Project]
		if ok && repo == "" {
			s.log.Warn("skipped, project has no repository", "task", t.ID, "project", t.Project)
		}
		if _, busy := s.running[t.ID]; busy || !ok || repo == "" || live(t) || slices.Contains(t.Tags, "epic") {
			continue
		}
		s.setLimit(t, repo)
		attempt, lastErr, wait := s.attempt(ctx, t)
		if wait || !s.ready(ctx, t) {
			continue
		}
		stop, cancel := context.WithCancelCause(ctx)
		s.running[t.ID] = &active{agent: name, actor: actorName(name, t.ID), stop: cancel}
		go func() {
			snap, err := s.runTask(ctx, stop, t, name, agent, repo, attempt, lastErr, s.log.With("task", t.ID))
			s.done <- outcome{t.ID, snap, err}
		}()
	}
}

// recoverOrphans reopens agent tasks left in in_progress with no run: after a kill of the daemon, a reboot
// or a failed write at the end of a run. Only an empty claim or our own actor's (live or expired) counts;
// another holder's task and a task without an agent: tag are not touched. A reopen of an empty claim is a failed attempt; of our own claim a pause only (the daemon died or
// stopped, which is not the agent's fault).
func (s *sched) recoverOrphans(ctx context.Context, repos map[string]string) {
	tasks, err := s.tr.List(ctx, []string{"in_progress"})
	if err != nil {
		s.log.Warn("in_progress not read, orphans skipped", "err", err)
		return
	}
	for _, t := range tasks {
		name, _, ok := s.agentFor(t)
		if _, busy := s.running[t.ID]; busy || !ok || repos[t.Project] == "" || slices.Contains(t.Tags, "epic") ||
			t.Claim != "" && t.Claim != actorName(name, t.ID) {
			continue
		}
		s.setLimit(t, repos[t.Project])
		if _, _, wait := s.attempt(ctx, t); wait {
			continue
		}
		reason := "task was left in_progress with no run"
		err = s.noteThen(ctx, t, fmt.Sprintf("%s. Back to %s.", reason, s.cfg.ReopenStatus), func() error { return s.tr.Reopen(ctx, t.ID) })
		switch {
		case errors.Is(err, errChanged):
		case err != nil:
			s.log.Error("could not reopen the orphan", "task", t.ID, "err", err)
			s.pause(t.ID) // the note is written: do not repeat it every tick
		default:
			s.log.Warn("orphan reopened", "task", t.ID, "claim", t.Claim)
			s.failed++
			own := t.Claim != ""
			t.Status, t.Claim = s.cfg.ReopenStatus, ""
			if own {
				s.pause(t.ID)
			} else if s.failure(t.ID, t, errors.New(reason)) {
				s.block(ctx, t.ID)
			}
		}
	}
}

func (s *sched) finish(ctx context.Context, o outcome) {
	s.running[o.id].stop(nil)
	delete(s.running, o.id)
	switch {
	case o.err == nil:
		s.clearTries(o.id)
	case errors.Is(o.err, errMoved):
		s.pause(o.id)
		s.log.Info("run stopped", "task", o.id, "outcome", "moved", "reason", "task changed hands, no attempt counted")
	case s.stopping:
		s.log.Info("run stopped", "task", o.id, "outcome", "shutdown")
	case errors.Is(o.err, errTracker):
		s.log.Error("tracker failed, no attempt counted", "task", o.id, "outcome", "tracker", "err", o.err)
		s.failed++
	default:
		s.log.Error("run failed", "task", o.id, "outcome", "failed", "err", o.err)
		s.failed++
		if s.failure(o.id, o.snap, o.err) {
			s.block(ctx, o.id)
		}
	}
}

func idNumber(id string) int {
	n, _ := strconv.Atoi(id[strings.LastIndex(id, "-")+1:])
	return n
}
