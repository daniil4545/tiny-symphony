//go:build live

package main

import (
	"os/exec"
	"strings"
	"testing"
)

// TestLiveClaude is the paid smoke test: the real claude CLI plans one toy task.
func TestLiveClaude(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH")
	}
	e := newEnv(t, `{}`)
	e.cfg.Agents = map[string]Agent{"claude": {Cmd: []string{"claude", "-p", "--permission-mode", "auto", "{prompt}"}}}
	e.cfg.RunTimeoutMinutes = 10
	backlogCmd(t, e.blDir, "task", "create", "Add a sum function", "-d",
		"Add a Go function Sum(a, b int) int to the toy repo, with a test.",
		"--ac", "Sum returns a+b", "--ac", "go test passes", "-l", "agent:claude", "--project", "toy")

	if failed := e.run(t.Context()); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	got := e.view(t, "T-1")
	t.Logf("status=%s plan=%q", got.Status, got.Plan)
	if got.Status != "Spec Review" || strings.TrimSpace(got.Plan) == "" {
		t.Fatalf("task = %+v", got)
	}
}
