package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchName(t *testing.T) {
	cases := map[string]TrackerTask{
		"T-12-fix-the-bug":               {ID: "T-12", Title: "Fix the  bug!"},
		"T-3-x":                          {ID: "t-3", Title: "X"},
		"T-1-a-b":                        {ID: "T-1", Title: "a/../b"},
		"T-1-" + strings.Repeat("a", 40): {ID: "T-1", Title: strings.Repeat("a", 60)},
		"T-12-task":                      {ID: "T-12", Title: "Исправить баг"},
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
	task := TrackerTask{ID: "T-1", Title: "Do it"}

	dir, branch, err := prepareWorkspace(t.Context(), root, repo, task)
	if err != nil || branch != "T-1-do-it" || dir != filepath.Join(root, branch) {
		t.Fatalf("%s %s %v", dir, branch, err)
	}
	if got := mustRun(t, dir, "branch", "--show-current"); got != branch {
		t.Fatalf("branch = %q", got)
	}
	// The state folder is outside git.
	write(t, filepath.Join(dir, stateDir, "log"), "x", 0o600)
	if got := mustRun(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree is dirty: %q", got)
	}
	if _, _, err = prepareWorkspace(t.Context(), root, repo, task); err != nil {
		t.Fatalf("reuse: %v", err)
	}
	ex, _ := os.ReadFile(mustRun(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
	if strings.Count(string(ex), stateDir+"/") != 1 {
		t.Fatalf("exclude: %q", ex)
	}
}

// A title with no Latin letters or digits still gives a worktree that is found by id.
func TestWorkspaceWithNonLatinTitleIsFound(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	dir, _, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-12", Title: "Исправить баг"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := findWorkspace(root, "T-12"); err != nil || got != dir {
		t.Fatalf("findWorkspace = %q, %v; want %q", got, err, dir)
	}
}

// spec: Loop
func TestWorkspaceIsFoundByIdNotTitle(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	dir, branch, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-1", Title: "Old title"})
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "work")
	dir10, _, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-10", Title: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	dir2, branch2, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-1", Title: "A human renamed it"})
	if err != nil || dir2 != dir || branch2 != branch || dir10 == dir {
		t.Fatalf("%s %s %v, want %s %s", dir2, branch2, err, dir, branch)
	}
	for id, want := range map[string]string{"T-1": dir, "T-10": dir10, "T-100": ""} {
		if got, err := findWorkspace(root, id); got != want || err != nil {
			t.Logf("%s: got %q %v, want %q", id, got, err, want)
			t.Fatal("T-1, T-10 and T-100 must not be mixed up")
		}
	}
	if got := mustRun(t, repo, "branch", "--format=%(refname:short)"); strings.Contains(got, "renamed") {
		t.Fatalf("a branch was made from the new title: %s", got)
	}
}

func TestPrepareWorkspaceReusesExistingBranch(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	mustRun(t, repo, "branch", "T-1-do-it")
	if _, _, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-1", Title: "Do it"}); err != nil {
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

// A subtask directory starts with the parent's id; the parent must not take it.
func TestSubtaskDirIsNotTakenByParent(t *testing.T) {
	repo, root := toyRepo(t), t.TempDir()
	sub, _, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-1.1", Title: "Part"})
	if err != nil {
		t.Fatal(err)
	}
	parent, _, err := prepareWorkspace(t.Context(), root, repo, TrackerTask{ID: "T-1", Title: "1 whole"})
	if err != nil || parent == sub {
		t.Fatalf("parent %q sub %q %v", parent, sub, err)
	}
	if got, _ := findWorkspace(root, "T-1.1"); got != sub {
		t.Fatalf("subtask lost its directory: %q", got)
	}
	// Directories without an id file are ambiguous: an error, no run.
	for _, d := range []string{sub, parent} {
		if err = os.Remove(filepath.Join(d, stateDir, "task-id")); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := findWorkspace(root, "T-1"); err == nil {
		t.Fatalf("two matches must fail, got %q", got)
	}
}
