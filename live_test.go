//go:build live

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// liveAgents are the commands the smoke test runs; an agent is verified only after a pass here.
var liveAgents = map[string][]string{
	"claude":   {"claude", "-p", "--permission-mode", "auto", "{prompt}"},
	"codex":    {"codex", "exec", "--sandbox", "workspace-write", "{prompt}"},
	"opencode": {"opencode", "run", "--auto", "{prompt}"},
	"kimi":     {"kimi", "-p", "{prompt}"},
	"pi":       {"pi", "-p", "{prompt}"},
}

func TestLiveClaude(t *testing.T) {
	liveRun(t, "claude")
}

func TestLiveCodex(t *testing.T) {
	liveRun(t, "codex")
}

func TestLiveOpencode(t *testing.T) {
	liveRun(t, "opencode")
}

func TestLiveKimi(t *testing.T) {
	liveRun(t, "kimi")
}

func TestLivePi(t *testing.T) {
	liveRun(t, "pi")
}

// liveRun is the paid smoke test: the real CLI plans one toy task.
func liveRun(t *testing.T, name string) {
	t.Helper()
	cmd := liveAgents[name]
	if _, err := exec.LookPath(cmd[0]); err != nil {
		t.Fatalf("%s CLI not on PATH", cmd[0])
	}
	e := newEnv(t, `{}`)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(e.cfg.WorkspaceRoot, "*", ".tiny-symphony", "*.log"))
		for _, l := range logs {
			out, _ := os.ReadFile(l) //nolint:gosec // a log in the test workspace
			t.Logf("%s:\n%.3000s", l, out)
		}
	})
	e.cfg.Agents = map[string]Agent{name: {Cmd: cmd}}
	e.cfg.RunTimeoutMinutes = 10
	backlogCmd(t, e.blDir, "task", "create", "Add a sum function", "-d",
		"Add a Go function Sum(a, b int) int to the toy repo, with a test.",
		"--ac", "Sum returns a+b", "--ac", "go test passes", "-l", "agent:"+name, "--project", "toy")

	if failed := e.run(t.Context()); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	got := e.view(t, "T-1")
	t.Logf("status=%s plan=%q", got.Status, got.Plan)
	if got.Status != "Spec Review" || strings.TrimSpace(got.Plan) == "" {
		t.Fatalf("task = %+v", got)
	}
}
