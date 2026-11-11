package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A stub agent that reports its start, then waits for a release file or for SIGTERM.
const holdAgent = `#!/bin/sh
touch "$STUB_DIR/start-$(basename "$PWD")"
trap 'touch "$STUB_DIR/term-$(basename "$PWD")"; exit 0' TERM
while [ ! -f "$STUB_DIR/release" ]; do sleep 0.05 & wait $!; done
cp "$STUB_RESULT" .tiny-symphony/result.json
`

func (e *env) sched(t *testing.T) (*sched, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STUB_DIR", dir)
	write(t, filepath.Join(e.configDir, "agent.sh"), holdAgent, 0o700)
	s := newSched(e.cfg, e.configDir, e.bl, slog.New(slog.DiscardHandler))
	s.poll, s.grace = 50*time.Millisecond, time.Second
	return s, dir
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func (s *sched) next() { s.finish(<-s.done) }

func TestTwoAgentsRunInParallelThirdWaits(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"p"}`)
	for _, title := range []string{"one", "two", "three"} {
		e.create(t, title, "agent:stub")
	}
	s, dir := e.sched(t)

	s.tick(t.Context())
	waitFor(t, "two agents", func() bool { return exists(dir+"/start-T-1-one") && exists(dir+"/start-T-2-two") })
	s.tick(t.Context()) // a second tick must not start T-1 again or take the third
	if len(s.running) != 2 || exists(dir+"/start-T-3-three") {
		t.Fatalf("running = %d, third started = %v", len(s.running), exists(dir+"/start-T-3-three"))
	}

	write(t, dir+"/release", "", 0o600)
	s.next()
	s.next()
	s.tick(t.Context()) // the slots are free: the third goes
	waitFor(t, "the third agent", func() bool { return exists(dir + "/start-T-3-three") })
	s.next()
	if s.failed != 0 || e.view(t, "T-3").Status != "Spec Review" {
		t.Fatalf("failed = %d, T-3 = %s", s.failed, e.view(t, "T-3").Status)
	}
}

func TestMovedCardStopsAgentInOneTick(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"p"}`)
	e.create(t, "moved", "agent:stub")
	s, dir := e.sched(t)

	s.tick(t.Context())
	waitFor(t, "the agent", func() bool { return exists(dir + "/start-T-1-moved") })
	backlogCmd(t, e.blDir, "task", "edit", "T-1", "-s", "Blocked")
	s.tick(t.Context()) // one tick: reconcile sees the new status and stops the run
	s.next()

	if !exists(dir+"/term-T-1-moved") || len(s.running) != 0 || s.failed != 0 {
		t.Fatalf("term = %v, running = %d, failed = %d", exists(dir+"/term-T-1-moved"), len(s.running), s.failed)
	}
	if got := e.view(t, "T-1"); got.Status != "Blocked" || got.Plan != "" {
		t.Fatalf("the result must not be applied: %+v", got)
	}
}

func TestTimeoutIsAFailure(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review"}`)
	e.create(t, "slow", "agent:stub")
	s, _ := e.sched(t)
	s.timeout = 200 * time.Millisecond

	s.tick(t.Context())
	s.next()
	if s.failed != 1 {
		t.Fatalf("failed = %d", s.failed)
	}
	write(t, e.cfg.WorkspaceRoot+"/x/"+stateDir+"/keep", "", 0o600)
	_, err := runAgent(t.Context(), e.cfg.WorkspaceRoot, e.cfg.WorkspaceRoot+"/x", Agent{Cmd: []string{"sleep", "30"}}, "p", 100*time.Millisecond, time.Second)
	if !errors.Is(err, errRunTimeout) {
		t.Fatalf("a timeout must be told from a crash: %v", err)
	}
	if _, err = runAgent(t.Context(), e.cfg.WorkspaceRoot, e.cfg.WorkspaceRoot+"/x", Agent{Cmd: []string{"false"}}, "p", time.Minute, time.Second); errors.Is(err, errRunTimeout) {
		t.Fatal("a crash is not a timeout")
	}
}

func TestSignalStopsRunsAndExits(t *testing.T) {
	e := newEnv(t, `{"status":"Spec Review","plan":"p"}`)
	e.create(t, "one", "agent:stub")
	s, dir := e.sched(t)
	ctx, cancel := context.WithCancel(t.Context())
	exit := make(chan int)
	go func() { exit <- s.loop(ctx, false) }()

	waitFor(t, "the agent", func() bool { return exists(dir + "/start-T-1-one") })
	cancel()
	<-exit
	if !exists(dir + "/term-T-1-one") {
		t.Fatal("the agent did not get SIGTERM before the loop exited")
	}
	if exists(e.cfg.WorkspaceRoot + "/T-1-one/" + stateDir + "/pid") {
		t.Fatal("pid file left")
	}
}

// A tracker read error skips the tick and does not stop the runs.
func TestTrackerErrorLeavesRunsAlone(t *testing.T) {
	stopped := false
	bl := &Backlog{run: func(context.Context, ...string) ([]byte, error) { return nil, errors.New("backlog down") }}
	s := newSched(&Config{MaxAgents: 2}, "", bl, slog.New(slog.DiscardHandler))
	s.running["T-1"] = &active{status: "To Do", stop: func(error) { stopped = true }}
	s.tick(t.Context())
	if stopped || len(s.running) != 1 {
		t.Fatal("the tick must be skipped")
	}
}

// spec: R6
func TestSecondInstanceDoesNotStart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work")
	first, err := lockWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockWorkspace(root); err == nil {
		_ = second.Close()
		t.Fatal("a second instance got the lock")
	}
	_ = first.Close()
	again, err := lockWorkspace(root)
	if err != nil {
		t.Fatalf("lock must be free after the first instance ends: %v", err)
	}
	_ = again.Close()
}
