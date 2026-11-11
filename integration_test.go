package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stubAgent = `#!/bin/sh
pwd -P > .tiny-symphony/cwd
printf '%s' "$1" > .tiny-symphony/prompt
cp "$STUB_RESULT" .tiny-symphony/result.json
`

type env struct {
	cfg       *Config
	configDir string
	bl        *Backlog
	repo      string
	blDir     string
}

func backlogCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "backlog", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BACKLOG_CWD="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("backlog %v: %v: %s", args, err, out)
	}
}

// newEnv makes a temp backlog, a toy repo and a config with a stub agent; the real git and backlog run.
func newEnv(t *testing.T, result string) *env {
	t.Helper()
	if _, err := exec.LookPath("backlog"); err != nil {
		t.Skip("backlog CLI not on PATH")
	}
	e := &env{repo: toyRepo(t), blDir: t.TempDir(), configDir: t.TempDir()}
	mustRun(t, e.blDir, "init", "-q")
	backlogCmd(t, e.blDir, "init", "toy", "--defaults", "--integration-mode", "none", "--task-prefix", "T", "--no-git")
	cfgFile := filepath.Join(e.blDir, "backlog", "config.yml")
	yml, _ := os.ReadFile(cfgFile)
	var out []string
	for line := range strings.SplitSeq(string(yml), "\n") {
		if strings.HasPrefix(line, "statuses:") {
			line = `statuses: ["To Do", "Spec Review", "In Progress", "Review", "Rework", "Blocked", "Done"]`
		}
		out = append(out, line)
	}
	write(t, cfgFile, strings.Join(out, "\n")+"projects: [\"toy\"]\n", 0o600)

	write(t, filepath.Join(e.configDir, "agent.sh"), stubAgent, 0o700)
	write(t, filepath.Join(e.configDir, "result.json"), result, 0o600)
	t.Setenv("STUB_RESULT", filepath.Join(e.configDir, "result.json"))
	for _, f := range []string{"to-do.md", "in-progress.md", "rework.md"} {
		text, err := os.ReadFile(filepath.Join("examples", "prompts", f))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(e.configDir, "prompts", f), string(text), 0o600)
	}
	e.cfg = &Config{
		BacklogDir: e.blDir, WorkspaceRoot: t.TempDir(), RunTimeoutMinutes: 1,
		Projects: map[string]Project{"toy": {Repo: e.repo}},
		Agents:   map[string]Agent{"stub": {Cmd: []string{filepath.Join(e.configDir, "agent.sh"), "{prompt}"}}},
	}
	e.bl = NewBacklog(e.blDir)
	return e
}

func (e *env) run(ctx context.Context) int {
	return runOnce(ctx, e.cfg, e.configDir, e.bl, slog.New(slog.DiscardHandler))
}

func (e *env) create(t *testing.T, title string, labels string) {
	t.Helper()
	backlogCmd(t, e.blDir, "task", "create", title, "-d", "the description", "--ac", "first", "--ac", "second",
		"-l", labels, "--project", "toy")
}

func (e *env) view(t *testing.T, id string) Task {
	t.Helper()
	task, err := e.bl.Load(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestRunOnceAppliesResult(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"- step one","notes":"a note","checked_ac":[2],"comment":""}`)
	e.create(t, "Write a plan", "agent:stub")
	e.create(t, "Human task", "quick")

	if failed := e.run(t.Context()); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	got := e.view(t, "T-1")
	if got.Status != "Spec Review" || got.Plan != "- step one" || got.Criteria[0].Checked || !got.Criteria[1].Checked {
		t.Fatalf("task = %+v", got)
	}

	// A task without agent:<name> is not touched: no worktree, status as it was.
	if other := e.view(t, "T-2"); other.Status != "To Do" {
		t.Fatalf("unlabeled task moved to %s", other.Status)
	}
	wt := filepath.Join(e.cfg.WorkspaceRoot, "T-1-write-a-plan")
	entries, _ := os.ReadDir(e.cfg.WorkspaceRoot)
	if len(entries) != 1 {
		t.Fatalf("worktrees: %v", entries)
	}
	cwd, _ := os.ReadFile(filepath.Join(wt, stateDir, "cwd"))
	want, _ := filepath.EvalSymlinks(wt)
	if strings.TrimSpace(string(cwd)) != want {
		t.Fatalf("agent cwd = %q, want %q", cwd, want)
	}
	prompt, _ := os.ReadFile(filepath.Join(wt, stateDir, "prompt"))
	if !strings.Contains(string(prompt), "T-1: Write a plan") || !strings.Contains(string(prompt), "2. second") {
		t.Fatalf("prompt = %q", prompt)
	}
	if st := mustRun(t, wt, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree dirty: %q", st)
	}

	// The task left To Do, so a second tick finds nothing.
	if failed := e.run(t.Context()); failed != 0 {
		t.Fatalf("second tick failed = %d", failed)
	}
}

func TestRunOnceRejectsInvalidTransition(t *testing.T) {
	e := newEnv(t, `{"status":"Done","plan":"should not be applied"}`)
	e.create(t, "Bad agent", "agent:stub")

	if failed := e.run(t.Context()); failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	got := e.view(t, "T-1")
	if got.Status != "To Do" || got.Plan != "" {
		t.Fatalf("nothing may be applied: %+v", got)
	}
}

func TestRunOnceSkipsUnknownProject(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review"}`)
	e.cfg.Projects = map[string]Project{}
	e.create(t, "No repo", "agent:stub")
	if failed := e.run(t.Context()); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	if e.view(t, "T-1").Status != "To Do" {
		t.Fatal("task must stay")
	}
}
