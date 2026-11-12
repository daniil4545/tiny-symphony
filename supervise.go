package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// procStart is the start time of a process as ps prints it, "" if it is gone: a reused pid has another one.
func procStart(ctx context.Context, pid int) string {
	out, err := exec.CommandContext(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output() //nolint:gosec // fixed binary, numeric arg
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// reapLeftovers kills the process groups that a crashed daemon left behind. A pid file holds the pid and the
// start time of the process; if the process is not provably the same one, the file is only removed.
func reapLeftovers(root string, log *slog.Logger) {
	files, _ := filepath.Glob(filepath.Join(root, "*", stateDir, "pid"))
	for _, f := range files {
		data, _ := os.ReadFile(f) //nolint:gosec // glob under workspace_root
		pidText, started, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
		pid, err := strconv.Atoi(pidText)
		pg, e := syscall.Getpgid(pid)
		switch {
		case err != nil || pid <= 1 || pid == os.Getpid() || pid == os.Getppid() || e != nil || pg != pid:
		case started == "" || procStart(context.Background(), pid) != started:
			log.Warn("pid file does not match the live process, not killing", "pid", pid, "file", f)
		default:
			log.Warn("killing a leftover agent group", "pid", pid, "file", f)
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = os.Remove(f)
	}
}

// housekeeping removes old run logs.
func (s *sched) housekeeping() {
	logs, _ := filepath.Glob(filepath.Join(s.cfg.WorkspaceRoot, "*", stateDir, "*.log"))
	cutoff := time.Now().AddDate(0, 0, -s.cfg.LogDays)
	for _, f := range logs {
		if fi, e := os.Stat(f); e == nil && fi.ModTime().Before(cutoff) {
			_ = os.Remove(f)
		}
	}
}

// limit is max_attempts of the task: the one of its project if the repository sets it, else the central one.
func (s *sched) limit(id string) int {
	if n := s.max[id]; n > 0 {
		return n
	}
	return s.cfg.MaxAttempts
}

// setLimit reads the project layer of the task's repository. A bad file leaves the central value;
// runTask reports it as a failed attempt.
func (s *sched) setLimit(t TrackerTask, repo string) {
	if c, err := withProjectLayer(s.cfg, repo); err == nil {
		s.max[t.ID] = c.MaxAttempts
	}
}

// tries is the failed-attempt record of a task, saved in .tiny-symphony/attempts of its worktree.
// Only the pause before the next attempt is in memory.
type tries struct {
	N    int    `json:"n"`
	Last string `json:"last"`
	next time.Time
	snap TrackerTask // the task as the last run ended: the guard of Block
}

func (s *sched) triesFile(id string) string {
	if dir, err := findWorkspace(s.cfg.WorkspaceRoot, id); err == nil && dir != "" {
		return filepath.Join(dir, stateDir, "attempts")
	}
	return ""
}

// getTries returns the record of the task, or nil; after a restart it comes from the file.
// The agent can write that file, so garbage or a count outside [0, max_attempts] means the limit is reached.
func (s *sched) getTries(id string) *tries {
	if tr := s.tries[id]; tr != nil {
		return tr
	}
	data, err := os.ReadFile(s.triesFile(id))
	if err != nil {
		return nil
	}
	tr := &tries{}
	if json.Unmarshal(data, tr) != nil || tr.N < 0 || tr.N > s.limit(id) {
		tr = &tries{N: s.limit(id), Last: "attempts file is damaged"}
	}
	s.tries[id] = tr
	return tr
}

func (s *sched) clearTries(id string) {
	delete(s.tries, id)
	if f := s.triesFile(id); f != "" {
		_ = os.Remove(f)
	}
}

// failure records a failed attempt and returns true when the task has used up max_attempts.
func (s *sched) failure(id string, snap TrackerTask, err error) bool {
	tr := s.getTries(id)
	if tr == nil {
		tr = &tries{}
		s.tries[id] = tr
	}
	tr.N++
	tr.Last, tr.snap = err.Error(), snap
	tr.next = time.Now().Add(s.backoff[max(min(tr.N, len(s.backoff)), 1)-1])
	if f := s.triesFile(id); f != "" {
		data, e := json.Marshal(tr)
		if e == nil {
			e = os.WriteFile(f, data, 0o600)
		}
		if e != nil {
			s.log.Warn("attempts not saved", "task", id, "err", e)
		}
	}
	return tr.N >= s.limit(id)
}

// pause is a stop without an attempt: the next backoff step, the count stays.
func (s *sched) pause(id string) {
	tr := s.getTries(id)
	if tr == nil {
		tr = &tries{}
		s.tries[id] = tr
	}
	tr.next = time.Now().Add(s.backoff[min(tr.N, len(s.backoff)-1)])
}

// errChanged means the task moved since the read that decided a write, so nothing was written.
var errChanged = errors.New("task changed since the deciding read")

// guarded runs a write only if the task still has an agent tag and the status, claim and external of snap.
func (s *sched) guarded(ctx context.Context, snap TrackerTask, write func() error) error {
	now, err := s.tr.Get(ctx, snap.ID)
	if err != nil {
		return fmt.Errorf("guard read of %s: %w", snap.ID, err)
	}
	if _, _, ok := s.agentFor(now); !ok || norm(now.Status) != norm(snap.Status) || now.Claim != snap.Claim || now.External != snap.External {
		s.log.Info("not writing, the task changed", "task", snap.ID, "status", now.Status, "claim", now.Claim)
		return errChanged
	}
	return write()
}

// noteThen writes a note and then runs the move, each after a guarding read.
func (s *sched) noteThen(ctx context.Context, snap TrackerTask, text string, move func() error) error {
	err := s.guarded(ctx, snap, func() error { return s.tr.Note(ctx, snap.ID, text) })
	if err == nil && move != nil {
		err = s.guarded(ctx, snap, move)
	}
	return err
}

// block gives up on a task: a note with the reason and the log path, then Block. The count stays until the write works.
func (s *sched) block(ctx context.Context, id string) {
	tr := s.getTries(id)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	snap, err := tr.snap, error(nil)
	if snap.ID == "" { // after a restart: the task as it is now
		snap, err = s.tr.Get(ctx, id)
	}
	if err == nil && live(snap) {
		s.log.Warn("not blocking, the task is held by someone", "task", id, "claim", snap.Claim)
		return
	}
	msg := fmt.Sprintf("gave up after %d failed attempts: %s", tr.N, tr.Last)
	if err == nil {
		err = s.noteThen(ctx, snap, msg, func() error { return s.tr.Block(ctx, id, fmt.Sprintf("%d failed attempts", tr.N)) })
	}
	switch {
	case errors.Is(err, errChanged):
		s.clearTries(id)
	case err != nil:
		s.log.Error("could not block the task", "task", id, "err", err)
	default:
		s.clearTries(id)
		s.log.Warn("task blocked", "task", id, "outcome", "blocked", "attempts", tr.N)
	}
}

// attempt is the number of the next run of the task and the last error; wait is true inside the pause.
func (s *sched) attempt(ctx context.Context, t TrackerTask) (n int, lastErr string, wait bool) {
	tr := s.getTries(t.ID)
	if tr == nil {
		return 1, "", false
	}
	if tr.N >= s.limit(t.ID) { // an earlier write of Block failed
		s.block(ctx, t.ID)
		return 0, "", true
	}
	return tr.N + 1, tr.Last, time.Now().Before(tr.next)
}
