package main

import "testing"

// spec: R7
// Cleanup waits for the watch while the PR is open, and does not wait for a task without a PR link.
func TestCleanupWaitsForWatchUnlessNoPR(t *testing.T) {
	fakeGH(t)
	e, s := watchEnv(t)
	_, withPR := closedTask(t, e, "https://github.com/o/r/pull/1")
	_, noPR := closedTask(t, e, "")
	s.cleanup(t.Context())
	if !exists(withPR) || exists(noPR) {
		t.Fatalf("with PR kept %v, without PR kept %v", exists(withPR), exists(noPR))
	}
}

// A dirty worktree stays; a merged branch goes with its worktree.
func TestCleanupKeepsDirtyTree(t *testing.T) {
	e, s := watchEnv(t)
	_, dir := closedTask(t, e, "")
	write(t, dir+"/new.txt", "x", 0o600)
	s.cleanup(t.Context())
	if !exists(dir) {
		t.Fatal("dirty worktree removed")
	}
}
