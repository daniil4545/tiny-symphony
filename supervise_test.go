package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The stub agent of the Review tests: commits a file, pushes the branch, then writes the result.
const workAgent = `#!/bin/sh
echo work > work.txt
git add work.txt
git -c user.name=t -c user.email=t@example.com commit -qm work
[ -n "$STUB_NOPUSH" ] || git push -q origin "HEAD:refs/heads/$(git branch --show-current)"
[ -z "$STUB_DIRTY" ] || echo junk > junk.txt
cp "$STUB_RESULT" .tiny-symphony/result.json
`

// failAgent logs every prompt it gets and fails.
const failAgent = `#!/bin/sh
printf '%s\n----\n' "$1" >> "$STUB_DIR/prompts"
exit 1
`

func (e *env) useAgent(t *testing.T, script string) {
	t.Helper()
	write(t, filepath.Join(e.configDir, "agent.sh"), script, 0o700)
}

// remote gives the toy repo a bare origin; gh is a stub that finds a PR only if its flag file exists.
func (e *env) remote(t *testing.T, pr bool) {
	t.Helper()
	bare := t.TempDir()
	mustRun(t, bare, "init", "-q", "--bare")
	mustRun(t, e.repo, "remote", "add", "origin", bare)
	bin := t.TempDir()
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\n[ -f \"$STUB_GH\" ]\n", 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	flag := filepath.Join(bin, "pr")
	if pr {
		write(t, flag, "", 0o600)
	}
	t.Setenv("STUB_GH", flag)
}

// drive ticks, waiting for the runs of each tick.
func drive(t *testing.T, s *sched, ticks int) {
	t.Helper()
	for range ticks {
		s.tick(t.Context())
		for len(s.running) > 0 {
			s.next()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) newSched() *sched {
	s := newSched(e.cfg, e.configDir, e.bl, slog.New(slog.DiscardHandler))
	s.backoff = []time.Duration{time.Millisecond}
	return s
}

// spec: S1
func TestCrashedAgentRetriesWithPauseAndReason(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"p"}`)
	e.useAgent(t, failAgent)
	e.create(t, "crashy", "agent:stub")
	t.Setenv("STUB_DIR", t.TempDir())
	s := e.newSched()
	s.backoff = []time.Duration{time.Hour}

	drive(t, s, 1)
	drive(t, s, 1) // inside the pause: no second start
	prompts, _ := os.ReadFile(os.Getenv("STUB_DIR") + "/prompts")
	if s.failed != 1 || strings.Count(string(prompts), "----") != 1 {
		t.Fatalf("failed = %d, prompts: %s", s.failed, prompts)
	}
	s.tries["T-1"].next = time.Now() // the pause is over
	drive(t, s, 1)
	prompts, _ = os.ReadFile(os.Getenv("STUB_DIR") + "/prompts")
	if !strings.Contains(string(prompts), "Attempt 2. The previous attempt failed: agent: agent exited") {
		t.Fatalf("the reason must reach the next prompt: %s", prompts)
	}
}

// spec: S7
func TestMaxAttemptsBlocksWithReasonAndLog(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"p"}`)
	e.useAgent(t, failAgent)
	e.create(t, "hopeless", "agent:stub")
	t.Setenv("STUB_DIR", t.TempDir())

	drive(t, e.newSched(), 6)
	got := e.view(t, "T-1")
	prompts, _ := os.ReadFile(os.Getenv("STUB_DIR") + "/prompts")
	if got.Status != "Blocked" || strings.Count(string(prompts), "----") != 3 {
		t.Fatalf("status = %s, starts = %d", got.Status, strings.Count(string(prompts), "----"))
	}
	if c := strings.Join(got.Comments, "\n"); !strings.Contains(c, "3 failed attempts") || !strings.Contains(c, ".log") {
		t.Fatalf("comment = %q", c)
	}
}

// spec: S4
func TestMissingOrBadResultIsAFailedAttempt(t *testing.T) {
	for name, agent := range map[string]string{
		"missing": "#!/bin/sh\ntrue\n",
		"bad":     "#!/bin/sh\necho '{' > .tiny-symphony/result.json\n",
	} {
		e := newEnv(t, `{}`)
		e.useAgent(t, agent)
		e.create(t, name, "agent:stub")
		if failed := e.run(t.Context()); failed != 1 || e.view(t, "T-1").Status != "To Do" {
			t.Fatalf("%s: failed = %d, status = %s", name, failed, e.view(t, "T-1").Status)
		}
	}
	if _, err := checkResult(Result{Status: "Blocked"}, Task{Status: "To Do"}); err == nil {
		t.Fatal("Blocked without a comment must fail")
	}
}

// spec: S5
func TestToDoWithoutPlanFails(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","notes":"no plan here"}`)
	e.create(t, "planless", "agent:stub")
	if failed := e.run(t.Context()); failed != 1 || e.view(t, "T-1").Status != "To Do" {
		t.Fatalf("failed = %d, status = %s", failed, e.view(t, "T-1").Status)
	}
}

// spec: S6
func TestReviewNeedsFinishedWork(t *testing.T) {
	cases := []struct {
		name, verify string
		pr           bool
		vars         map[string]string
		want         string
	}{
		{"all good", "true", true, nil, "Review"},
		{"uncommitted", "true", true, map[string]string{"STUB_DIRTY": "1"}, "To Do"},
		{"not pushed", "true", true, map[string]string{"STUB_NOPUSH": "1"}, "To Do"},
		{"no PR", "true", false, nil, "To Do"},
		{"verify fails", "false", true, nil, "To Do"},
	}
	for _, c := range cases {
		e := newEnv(t, `{"status":"Review"}`)
		e.useAgent(t, workAgent)
		e.remote(t, c.pr)
		e.cfg.Projects["toy"] = Project{Repo: e.repo, Verify: c.verify}
		for k, v := range c.vars {
			t.Setenv(k, v)
		}
		e.create(t, c.name, "agent:stub,quick")
		e.run(t.Context())
		if got := e.view(t, "T-1").Status; got != c.want {
			t.Errorf("%s: status = %s, want %s", c.name, got, c.want)
		}
		for k := range c.vars {
			t.Setenv(k, "")
		}
	}
}

// spec: S8
func TestLeftoverGroupIsKilledOnStart(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "T-1-x", stateDir, "keep"), "", 0o600)
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(root, "T-1-x", stateDir, "pid")
	write(t, pidFile, strconv.Itoa(cmd.Process.Pid)+"\n"+procStart(t.Context(), cmd.Process.Pid)+"\n", 0o600)
	reapLeftovers(root, slog.New(slog.DiscardHandler))
	if err := cmd.Wait(); err == nil {
		t.Fatal("the leftover process survived")
	}
	if exists(pidFile) {
		t.Fatal("pid file left")
	}
}

// A pid file whose start time does not match (a reused pid) must not kill the live stranger.
func TestStalePidFileDoesNotKillStranger(t *testing.T) {
	root := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	for _, body := range []string{"\n", "Thu Jan  1 00:00:00 1970\n"} {
		pidFile := filepath.Join(root, "T-1-x", stateDir, "pid")
		write(t, pidFile, strconv.Itoa(cmd.Process.Pid)+"\n"+body, 0o600)
		reapLeftovers(root, slog.New(slog.DiscardHandler))
		if syscall.Kill(cmd.Process.Pid, 0) != nil || exists(pidFile) {
			t.Fatalf("stranger killed or pid file left (start %q)", body)
		}
	}
}

func TestBlockLeavesAMovedCardAlone(t *testing.T) {
	e := newEnv(t, `{}`)
	e.create(t, "moved", "agent:stub")
	backlogCmd(t, e.blDir, "task", "edit", "T-1", "-s", "Spec Review")
	s := e.newSched()
	s.tries["T-1"] = &tries{n: 3, status: "To Do", last: "boom"}
	s.block(t.Context(), "T-1")
	if got := e.view(t, "T-1"); got.Status != "Spec Review" || len(got.Comments) != 0 || len(s.tries) != 0 {
		t.Fatalf("status = %s, comments = %v", got.Status, got.Comments)
	}
}

// spec: R7
func TestDoneCleanupKeepsUnmergedBranchAndNeverPushes(t *testing.T) {
	e := newEnv(t, `{}`)
	e.remote(t, false)
	for _, title := range []string{"unmerged", "merged", "running"} {
		e.create(t, title, "agent:stub")
	}
	backlogCmd(t, e.blDir, "task", "edit", "T-1", "-s", "Done")
	backlogCmd(t, e.blDir, "task", "edit", "T-2", "-s", "Done")
	dirs := map[string]string{}
	for _, id := range []string{"T-1", "T-2", "T-3"} {
		dir, _, err := prepareWorkspace(t.Context(), e.cfg.WorkspaceRoot, e.repo, e.view(t, id))
		if err != nil {
			t.Fatal(err)
		}
		dirs[id] = dir
	}
	write(t, filepath.Join(dirs["T-1"], "new.txt"), "x\n", 0o600)
	mustRun(t, dirs["T-1"], "add", "new.txt")
	mustRun(t, dirs["T-1"], "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "wip")

	mustRun(t, dirs["T-1"], "push", "-q", "-u", "origin", "HEAD") // upstream equals the branch: `-d` alone would delete it
	s := e.newSched()
	s.running["T-2"] = &active{stop: func(error) {}}
	s.housekeeping(t.Context())
	if !exists(dirs["T-2"]) {
		t.Fatal("a task with a running agent keeps its worktree")
	}
	delete(s.running, "T-2")
	s.housekeeping(t.Context())
	if exists(dirs["T-1"]) || exists(dirs["T-2"]) || !exists(dirs["T-3"]) {
		t.Fatal("worktrees of Done tasks go, others stay")
	}
	branches := mustRun(t, e.repo, "branch", "--format=%(refname:short)")
	if !strings.Contains(branches, "T-1-unmerged") || strings.Contains(branches, "T-2-merged") {
		t.Fatalf("branches: %s", branches)
	}
	origin := mustRun(t, e.repo, "remote", "get-url", "origin")
	if refs := mustRun(t, origin, "for-each-ref"); strings.Count(refs, "\n") != 0 { // only the test's own push
		t.Fatalf("the daemon pushed: %s", refs)
	}
}

func TestOldLogsAreRotated(t *testing.T) {
	e := newEnv(t, `{}`)
	state := filepath.Join(e.cfg.WorkspaceRoot, "T-1-x", stateDir)
	old, fresh := filepath.Join(state, "old.log"), filepath.Join(state, "new.log")
	write(t, old, "x", 0o600)
	write(t, fresh, "x", 0o600)
	past := time.Now().AddDate(0, 0, -15)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	e.newSched().housekeeping(t.Context())
	if exists(old) || !exists(fresh) {
		t.Fatalf("old exists = %v, fresh exists = %v", exists(old), exists(fresh))
	}
}
