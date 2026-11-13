package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// spec: S6
func TestLeftoverGroupIsKilledOnStart(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "T-1-x", stateDir, "keep"), "", 0o600)
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "T-1-x", stateDir, "pid")
	write(t, pidFile, strconv.Itoa(cmd.Process.Pid)+"\n"+procStart(t.Context(), cmd.Process.Pid)+"\n", 0o600)
	reapLeftovers(root, slog.New(slog.DiscardHandler))
	if err := cmd.Wait(); err == nil {
		t.Fatal("the leftover process survived")
	}
	if exists(pidFile) {
		t.Fatal("pid file left")
	}
}

// A pid file whose start time does not match (a reused pid) must not kill the live stranger.
func TestStalePidFileDoesNotKillStranger(t *testing.T) {
	root := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for _, body := range []string{"\n", "Thu Jan  1 00:00:00 1970\n"} {
		pidFile := filepath.Join(root, "T-1-x", stateDir, "pid")
		write(t, pidFile, strconv.Itoa(cmd.Process.Pid)+"\n"+body, 0o600)
		reapLeftovers(root, slog.New(slog.DiscardHandler))
		if syscall.Kill(cmd.Process.Pid, 0) != nil || exists(pidFile) {
			t.Fatalf("stranger killed or pid file left (start %q)", body)
		}
	}
}

func TestOldLogsAreRotated(t *testing.T) {
	e := newEnv(t, "noop")
	state := filepath.Join(e.cfg.WorkspaceRoot, "T-1-x", stateDir)
	old, fresh := filepath.Join(state, "old.log"), filepath.Join(state, "new.log")
	write(t, old, "x", 0o600)
	write(t, fresh, "x", 0o600)
	past := time.Now().AddDate(0, 0, -15)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	e.sched().housekeeping()
	if exists(old) || !exists(fresh) {
		t.Fatalf("old exists = %v, fresh exists = %v", exists(old), exists(fresh))
	}
}

func attemptsFile(t *testing.T, e *env, id string) string {
	t.Helper()
	dir, err := findWorkspace(e.cfg.WorkspaceRoot, id)
	if err != nil || dir == "" {
		t.Fatalf("workspace of %s: %q %v", id, dir, err)
	}
	return filepath.Join(dir, stateDir, "attempts")
}

// spec: S5
// Every pass is a new process, like `run --once` from cron: the count survives in the worktree.
func TestAttemptsSurviveRestart(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "hopeless", "agent:stub")
	for i := range 5 {
		drive(t, e.sched(), 1)
		if want := min(i+1, 3); len(e.starts(t)) != want {
			t.Fatalf("pass %d: starts = %d, want %d", i+1, len(e.starts(t)), want)
		}
	}
	if e.get(t, id).Status != "blocked" || exists(attemptsFile(t, e, id)) {
		t.Fatalf("status = %s, the attempts file must go with Block", e.get(t, id).Status)
	}
}

// The agent can write the attempts file: garbage or a count out of range means the limit is reached.
func TestDamagedAttemptsFileMeansLimitReached(t *testing.T) {
	for _, body := range []string{`{"n":-5}`, `{"n":999999999999}`, `not json`, `{"n":"x"}`} {
		e := newEnv(t, "noop")
		id := e.create(t, "tampered", "agent:stub")
		task := e.get(t, id)
		if _, _, err := prepareWorkspace(t.Context(), e.cfg.WorkspaceRoot, e.repo, task); err != nil {
			t.Fatal(err)
		}
		write(t, attemptsFile(t, e, id), body, 0o600)
		drive(t, e.sched(), 2)
		if got := e.get(t, id); len(e.starts(t)) != 0 || got.Status != "blocked" ||
			!strings.Contains(lastNote(t, e, id), "attempts file is damaged") {
			t.Fatalf("%s: status %s, starts %v", body, got.Status, e.starts(t))
		}
	}
}

// A tracker failure after the run is not an attempt of the agent, and a failing write is retried by the next tick.
func TestTrackerFailureIsNotAnAttempt(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "flaky", "agent:stub")
	s := e.sched()
	s.cfg.MaxAttempts = 1
	s.finish2(t, outcome{id: id, err: errTracker})
	if s.failed != 1 || s.tries[id] != nil || e.get(t, id).Status != "open" {
		t.Fatalf("failed %d, tries %+v", s.failed, s.tries[id])
	}
}

func (s *sched) finish2(t *testing.T, o outcome) {
	t.Helper()
	s.running[o.id] = &active{stop: func(error) {}}
	s.finish(context.Background(), o)
}

// trackerRetry gives up after three tries and then the error counts as a failed attempt.
func TestTrackerRetryCountsAfterThreeFailures(t *testing.T) {
	s := newSched(&Config{}, nil, slog.New(slog.DiscardHandler))
	s.trackerPause = 0
	calls := 0
	err := s.trackerRetry(func() error { calls++; return errTracker })
	if calls != 3 || err == nil || errors.Is(err, errTracker) {
		t.Fatalf("calls %d, err %v", calls, err)
	}
}
