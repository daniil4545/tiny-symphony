package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH puts a gh on PATH that logs its calls and prints $GH_JSON, or fails when that file is "fail".
func fakeGH(t *testing.T) (setPR func(string), calls func() int) {
	t.Helper()
	bin, state := t.TempDir(), t.TempDir()
	write(t, filepath.Join(bin, "gh"), `#!/bin/sh
echo "$PWD $*" >> "$GH_DIR/calls"
[ "$(cat "$GH_DIR/pr")" = fail ] && { echo boom >&2; exit 1; }
cat "$GH_DIR/pr"
`, 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_DIR", state)
	setPR = func(s string) { write(t, filepath.Join(state, "pr"), s, 0o600) }
	calls = func() int {
		data, _ := os.ReadFile(filepath.Join(state, "calls"))
		return strings.Count(string(data), "\n")
	}
	return setPR, calls
}

// closedTask is a task with a worktree and a PR link, moved to closed.
func closedTask(t *testing.T, e *env, link string) (id, dir string) {
	t.Helper()
	id = e.create(t, "Do it", "agent:stub")
	task := e.get(t, id)
	dir, _, err := prepareWorkspace(t.Context(), e.cfg.WorkspaceRoot, e.repo, task)
	if err != nil {
		t.Fatal(err)
	}
	tixDo(t, e.tx, "", "check", id, "1")
	if link != "" {
		tixDo(t, e.tx, "", "set", id, "external="+link)
	}
	tixDo(t, e.tx, "", "move", id, "closed")
	return id, dir
}

func watchEnv(t *testing.T) (*env, *sched) {
	t.Helper()
	e := newEnv(t, "noop")
	e.cfg.WatchPR = WatchPR{Enabled: true, PollMinutes: 5, MaxReopens: 2}
	e.cfg.Cleanup = Cleanup{Enabled: true, LogDays: 14}
	s := e.sched()
	t.Cleanup(func() { watchLast.Delete(s) })
	return e, s
}

// round forgets the poll time, so the next watchPRs polls.
func round(t *testing.T, s *sched) {
	t.Helper()
	watchLast.Delete(s)
	s.watchPRs(t.Context())
}

const (
	prRed     = `{"state":"OPEN","mergeStateStatus":"BLOCKED","statusCheckRollup":[{"name":"build","conclusion":"FAILURE"}]}`
	prPending = `{"state":"OPEN","mergeStateStatus":"BLOCKED","statusCheckRollup":[{"name":"build","status":"IN_PROGRESS","conclusion":""},{"name":"lint","conclusion":"SKIPPED"}]}`
	prDirty   = `{"state":"OPEN","mergeStateStatus":"DIRTY","statusCheckRollup":[]}`
	prMerged  = `{"state":"MERGED","mergedAt":"2026-11-12T10:00:00Z","mergeCommit":{"oid":"abc123"},"mergeStateStatus":"UNKNOWN","statusCheckRollup":[]}`
	prClosed  = `{"state":"CLOSED","mergedAt":null,"mergeStateStatus":"UNKNOWN","statusCheckRollup":[]}`
)

// spec: R7, R5, R8
// Acceptance 8: a red check gives a note and Reopen until max_reopens, then one note; pending gives nothing.
func TestWatchRedReopensThenOneLimitNote(t *testing.T) {
	setPR, calls := fakeGH(t)
	e, s := watchEnv(t)
	id, _ := closedTask(t, e, "https://github.com/o/r/pull/1")
	setPR(prPending)
	round(t, s)
	if calls() != 1 || len(e.daemon(t)) != 0 {
		t.Fatalf("pending: gh calls %d, daemon writes %v", calls(), e.daemon(t))
	}
	for i := 1; i <= 2; i++ {
		setPR(prRed)
		round(t, s)
		if got := e.get(t, id).Status; got != "open" {
			t.Fatalf("reopen %d: status %q", i, got)
		}
		tixDo(t, e.tx, "", "move", id, "closed")
	}
	before := len(e.daemon(t))
	for range 3 {
		round(t, s)
	}
	if got := len(e.daemon(t)) - before; got != 1 {
		t.Fatalf("limit: %d daemon writes, want 1 note", got)
	}
	if got := e.get(t, id).Status; got != "closed" || !strings.Contains(lastNote(t, e, id), "Needs a human") {
		t.Fatalf("status %q, not a limit note", got)
	}
}

func TestWatchDirtyReopens(t *testing.T) {
	setPR, _ := fakeGH(t)
	e, s := watchEnv(t)
	id, _ := closedTask(t, e, "https://github.com/o/r/pull/1")
	setPR(prDirty)
	round(t, s)
	if e.get(t, id).Status != "open" || !strings.Contains(lastNote(t, e, id), "conflicts") {
		t.Fatalf("not reopened with a reason: %s", lastNote(t, e, id))
	}
}

// spec: R7
// Acceptance 8: merged gives a note and the worktree removed; closed gives one note; a restart repeats nothing.
func TestWatchMergedAndClosedOnceAcrossRestart(t *testing.T) {
	setPR, _ := fakeGH(t)
	e, s := watchEnv(t)
	merged, mdir := closedTask(t, e, "https://github.com/o/r/pull/1")
	setPR(prMerged)
	s.watchPRs(t.Context())
	if !strings.Contains(lastNote(t, e, merged), "PR merged abc123") {
		t.Fatalf("no merged note: %s", lastNote(t, e, merged))
	}
	shut, sdir := closedTask(t, e, "https://github.com/o/r/pull/2")
	setPR(prClosed)
	round(t, s)
	if !strings.Contains(lastNote(t, e, shut), "closed without merge") {
		t.Fatalf("no closed note: %s", lastNote(t, e, shut))
	}
	n := len(e.daemon(t))
	s2 := e.sched() // a restart: the marks come from the files
	t.Cleanup(func() { watchLast.Delete(s2) })
	setPR(prMerged)
	s2.watchPRs(t.Context())
	if got := len(e.daemon(t)); got != n {
		t.Fatalf("restart wrote %d more notes", got-n)
	}
	s2.cleanup(t.Context())
	if exists(mdir) || exists(sdir) {
		t.Fatalf("worktrees kept: merged %v, closed %v", exists(mdir), exists(sdir))
	}
}

// Acceptance 8: gh failing gives no tracker write.
func TestWatchGHFailureWritesNothing(t *testing.T) {
	setPR, calls := fakeGH(t)
	e, s := watchEnv(t)
	closedTask(t, e, "https://github.com/o/r/pull/1")
	setPR("fail")
	round(t, s)
	if calls() != 1 || len(e.daemon(t)) != 0 {
		t.Fatalf("gh calls %d, daemon writes %v", calls(), e.daemon(t))
	}
}

// The poll interval is kept inside watchPRs.
func TestWatchPollsOncePerInterval(t *testing.T) {
	setPR, calls := fakeGH(t)
	e, s := watchEnv(t)
	closedTask(t, e, "https://github.com/o/r/pull/1")
	setPR(prPending)
	s.watchPRs(t.Context())
	s.watchPRs(t.Context())
	if calls() != 1 {
		t.Fatalf("gh called %d times in one interval", calls())
	}
}

// Acceptance 8: with the watch off gh is never called, and cleanup does not wait for the watch.
func TestWatchOffNeverCallsGH(t *testing.T) {
	_, calls := fakeGH(t)
	e, s := watchEnv(t)
	e.cfg.WatchPR.Enabled = false
	_, dir := closedTask(t, e, "https://github.com/o/r/pull/1")
	s.cleanup(t.Context())
	if calls() != 0 || exists(dir) {
		t.Fatalf("gh calls %d, worktree kept %v", calls(), exists(dir))
	}
}
