package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchName(t *testing.T) {
	cases := map[string]Task{
		"T-12-fix-the-bug":               {ID: "T-12", Title: "Fix the  bug!"},
		"T-3-x":                          {ID: "t-3", Title: "X"},
		"T-1-a-b":                        {ID: "T-1", Title: "a/../b"},
		"T-1-" + strings.Repeat("a", 40): {ID: "T-1", Title: strings.Repeat("a", 60)},
	}
	for want, task := range cases {
		if got := branchName(task); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestInside(t *testing.T) {
	for path, want := range map[string]bool{"/r/a": true, "/r/a/b": true, "/r": false, "/r/../x": false, "/rx": false, "/": false} {
		if inside("/r", path) != want {
			t.Errorf("inside(/r, %s) != %v", path, want)
		}
	}
}

func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func toyRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	mustRun(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "README"), "toy\n", 0o600)
	mustRun(t, repo, "add", "README")
	mustRun(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init")
	return repo
}

func TestPrepareWorkspace(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	task := Task{ID: "T-1", Title: "Do it"}

	dir, branch, err := prepareWorkspace(t.Context(), root, repo, task)
	if err != nil || branch != "T-1-do-it" || dir != filepath.Join(root, branch) {
		t.Fatalf("%s %s %v", dir, branch, err)
	}
	if got := mustRun(t, dir, "branch", "--show-current"); got != branch {
		t.Fatalf("branch = %q", got)
	}
	// The state folder is outside git, and a stale result does not survive into the next run.
	write(t, filepath.Join(dir, stateDir, "result.json"), "{}", 0o600)
	if got := mustRun(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree is dirty: %q", got)
	}
	if _, _, err = prepareWorkspace(t.Context(), root, repo, task); err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if _, err = os.Stat(filepath.Join(dir, stateDir, "result.json")); err == nil {
		t.Fatal("stale result.json was kept")
	}
	ex, _ := os.ReadFile(mustRun(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	if strings.Count(string(ex), stateDir+"/") != 1 {
		t.Fatalf("exclude: %q", ex)
	}
}

func TestPrepareWorkspaceReusesExistingBranch(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	mustRun(t, repo, "branch", "T-1-do-it")
	if _, _, err := prepareWorkspace(t.Context(), root, repo, Task{ID: "T-1", Title: "Do it"}); err != nil {
		t.Fatal(err)
	}
}

func TestExcludeKeepsUserLine(t *testing.T) {
	repo := toyRepo(t)
	path := filepath.Join(repo, ".git", "info", "exclude")
	write(t, path, "*.log", 0o600)
	if err := excludeStateDir(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "*.log\n"+stateDir+"/\n" {
		t.Fatalf("exclude = %q", got)
	}
}
