//go:build live

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveAgents are the commands the smoke test runs; an agent is verified only after a pass here.
var liveAgents = map[string][]string{
	"claude":   {"claude", "-p", "--permission-mode", "auto", "{prompt}"},
	"codex":    {"codex", "exec", "--sandbox", "danger-full-access", "{prompt}"},
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

// liveRun is the paid smoke test: the real CLI does one small task. With LIVE_REPO (a GitHub URL) the task
// runs in a fresh clone of it and must be handed over with a PR, which is closed afterwards; without it in a
// local toy repo, where only the handover is checked.
func liveRun(t *testing.T, name string) {
	t.Helper()
	cmd := liveAgents[name]
	if _, err := exec.LookPath(cmd[0]); err != nil {
		t.Fatalf("%s CLI not on PATH", cmd[0])
	}
	e := newEnv(t, "")
	project, body := "toy", "- [ ] go test passes\nAdd a Go function Sum(a, b int) int to the toy repo, with a test.\n"
	if url := os.Getenv("LIVE_REPO"); url != "" {
		clone := filepath.Join(t.TempDir(), "live")
		if out, err := exec.CommandContext(t.Context(), "git", "clone", "-q", url, clone).CombinedOutput(); err != nil {
			t.Fatalf("clone %s: %v: %s", url, err, out)
		}
		tixDo(t, e.tx, "", "projects", "add", "live", clone)
		project = "live"
		file := fmt.Sprintf("smoke/%s-%d.md", name, time.Now().Unix())
		body = "- [ ] " + file + " exists\nAdd the file " + file + " with one line: the agent name and today's date. " +
			"Open the PR against main. This is a smoke test of tiny-symphony: the PR is closed after the run. " +
			"The criterion \"a\" is a placeholder of the test harness, skip it.\n"
	}
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
	e.cfg.Agents = map[string]Agent{}
	e.cfg.RunTimeoutMinutes = 8 // under the 10 min default of go test
	id := e.createIn(t, "Smoke "+name, project, body, "agent:"+name)
	e.cfg.Agents[name] = Agent{Cmd: cmd}

	failed := e.run(t)
	got := e.get(t, id)
	t.Logf("status=%s external=%q", got.Status, got.External)
	if project == "live" && strings.HasPrefix(got.External, "https://") {
		t.Cleanup(func() {
			out, err := exec.Command("gh", "pr", "close", got.External, "--delete-branch").CombinedOutput() //nolint:gosec,noctx // the test PR of this run
			t.Logf("close %s: %v %s", got.External, err, out)
		})
	}
	if failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	if got.Status != e.cfg.HandoffStatus || got.External == "" {
		t.Fatalf("task = %+v", got)
	}
	actor := "Actor: " + actorName(name, id)
	out, err := exec.CommandContext(t.Context(), "git", "-C", e.home, "log", "--format=%B").Output()
	if err != nil || !strings.Contains(string(out), actor) {
		t.Fatalf("no store commit by %s: %v", actor, err)
	}
}
