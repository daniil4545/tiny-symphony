package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func TestRenderPromptStrict(t *testing.T) {
	dir := t.TempDir()
	task := Task{ID: "T-1", Title: "t", Criteria: []Criterion{{Index: 1, Text: "do a"}}}

	write(t, filepath.Join(dir, "ok.md"), "{{.id}} {{.attempt}}|{{.last_error}}|{{range .criteria}}{{.index}}={{.text}}{{end}}", 0o600)
	got, err := renderPrompt(filepath.Join(dir, "ok.md"), task, "T-1-t", 2, "boom")
	if err != nil || got != "T-1 2|boom|1=do a" {
		t.Fatalf("got %q, %v", got, err)
	}

	write(t, filepath.Join(dir, "bad.md"), "{{.no_such_variable}}", 0o600)
	if _, err = renderPrompt(filepath.Join(dir, "bad.md"), task, "b", 1, ""); err == nil {
		t.Fatal("an unknown variable must fail the render")
	}
}

func TestPromptFile(t *testing.T) {
	if got := promptFile("/c", "In Progress"); got != "/c/prompts/in-progress.md" {
		t.Fatal(got)
	}
}

// The prompt reaches the agent as one argv element, untouched by any shell.
// spec: R5, R1
func TestPromptPassesWithoutShell(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "T-1-x")
	write(t, filepath.Join(dir, stateDir, "keep"), "", 0o600)
	script := filepath.Join(root, "agent.sh")
	write(t, script, "#!/bin/sh\nprintf '%s' \"$#\" > argc\nprintf '%s' \"$1\" > arg1\npwd -P > cwd\n", 0o700)
	prompt := "a b; $(touch pwned) `touch pwned2` \"q\" 'r'\n- line"

	if _, err := runAgent(t.Context(), root, dir, Agent{Cmd: []string{script, "{prompt}"}}, prompt, time.Minute, time.Second); err != nil {
		t.Fatal(err)
	}
	argc, _ := os.ReadFile(filepath.Join(dir, "argc"))
	arg1, _ := os.ReadFile(filepath.Join(dir, "arg1"))
	if string(argc) != "1" || string(arg1) != prompt {
		t.Fatalf("argc=%q arg1=%q", argc, arg1)
	}
	for _, f := range []string{"pwned", "pwned2"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			t.Fatalf("shell ran: %s exists", f)
		}
	}
	cwd, _ := os.ReadFile(filepath.Join(dir, "cwd"))
	want, _ := filepath.EvalSymlinks(dir)
	if strings.TrimSpace(string(cwd)) != want {
		t.Fatalf("cwd = %q, want the worktree %q", cwd, want)
	}
	if _, err := os.Stat(filepath.Join(dir, stateDir, "pid")); err == nil {
		t.Fatal("pid file must be removed after the run")
	}
}

// spec: R1
func TestRunAgentRefusesOutsideRoot(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{other, root, filepath.Join(root, "..")} {
		if _, err := runAgent(t.Context(), root, dir, Agent{Cmd: []string{"true"}}, "p", time.Minute, time.Second); err == nil {
			t.Fatalf("started an agent in %s", dir)
		}
	}
}

func TestRunAgentTimeoutKillsGroup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "w")
	write(t, filepath.Join(dir, stateDir, "keep"), "", 0o600)
	start := time.Now()
	_, err := runAgent(t.Context(), root, dir, Agent{Cmd: []string{"sh", "-c", "sleep 30 & wait"}}, "p", 200*time.Millisecond, time.Second)
	if err == nil || time.Since(start) > 15*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

// spec: R4
func TestReadResult(t *testing.T) {
	dir := t.TempDir()
	if _, err := readResult(dir); err == nil {
		t.Fatal("missing file must fail")
	}
	write(t, filepath.Join(dir, stateDir, "result.json"), `{"plan":"p"}`, 0o600)
	if _, err := readResult(dir); err == nil {
		t.Fatal("status is required")
	}
	write(t, filepath.Join(dir, stateDir, "result.json"), `{"status":"Review","checked_ac":[1]}`, 0o600)
	if r, err := readResult(dir); err != nil || r.Status != "Review" || r.CheckedAC[0] != 1 {
		t.Fatalf("%+v %v", r, err)
	}
}
