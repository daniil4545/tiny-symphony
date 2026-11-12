package main

import (
	"path/filepath"
	"testing"
)

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

// spec: R7
// A branch with unmerged work stays even when its upstream has it, as after `git push -u` of a PR closed unmerged.
func TestCleanupKeepsUnmergedBranchWithUpstream(t *testing.T) {
	e, s := watchEnv(t)
	e.cfg.WatchPR.Enabled = false
	_, dir := closedTask(t, e, "")
	branch := filepath.Base(dir)
	remote := t.TempDir()
	if _, err := git(t.Context(), remote, "init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	if _, err := git(t.Context(), e.repo, "remote", "add", "origin", remote); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "work.txt"), "x", 0o600)
	for _, args := range [][]string{{"add", "work.txt"}, {"commit", "-q", "-m", "work"}, {"push", "-q", "-u", "origin", branch}} {
		if _, err := git(t.Context(), dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	s.cleanup(t.Context())
	if exists(dir) {
		t.Fatal("worktree of a clean tree was kept")
	}
	if _, err := git(t.Context(), e.repo, "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		t.Fatalf("unmerged branch %s deleted: %v", branch, err)
	}
}
