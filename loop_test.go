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

func (s *sched) next() { s.finish(context.Background(), <-s.done) }

func TestTwoAgentsRunInParallelThirdWaits(t *testing.T) {
	e := newEnv(t, "hold")
	e.create(t, "one", "agent:stub")
	e.create(t, "two", "agent:stub")
	e.create(t, "three", "agent:stub")
	s := e.sched()
	s.backoff = []time.Duration{time.Hour} // the stopped runs wait, so the third goes next

	s.tick(t.Context())
	waitFor(t, "two agents", func() bool { return len(e.starts(t)) == 2 })
	s.tick(t.Context()) // a second tick must not start a run again or take the third
	if len(s.running) != 2 || len(e.starts(t)) != 2 {
		t.Fatalf("running = %d, starts = %v", len(s.running), e.starts(t))
	}
	stopAll := func() {
		for _, r := range s.running {
			r.stop(errMoved)
		}
		for len(s.running) > 0 {
			s.next()
		}
	}
	stopAll()
	s.tick(t.Context()) // the slots are free: the third goes
	waitFor(t, "the third agent", func() bool { return len(e.starts(t)) == 3 })
	stopAll()
}

// spec: S2
func TestTimeoutIsAFailure(t *testing.T) {
	e := newEnv(t, "hold")
	id := e.create(t, "slow", "agent:stub")
	s := e.sched()
	s.timeout = 300 * time.Millisecond

	s.tick(t.Context())
	s.next()
	if s.failed != 1 || e.get(t, id).Status != "open" || s.tries[id].N != 1 {
		t.Fatalf("failed = %d, task %+v", s.failed, e.get(t, id))
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
	e := newEnv(t, "hold")
	id := e.create(t, "one", "agent:stub")
	s := e.sched()
	ctx, cancel := context.WithCancel(t.Context())
	exit := make(chan int)
	go func() { exit <- s.loop(ctx, false) }()

	waitFor(t, "the agent", func() bool { return len(e.starts(t)) == 1 })
	cancel()
	<-exit
	if s.failed != 0 {
		t.Fatalf("a shutdown is not a failure: failed = %d", s.failed)
	}
	if !exists(filepath.Join(e.dir, "term-"+id)) {
		t.Fatal("the agent did not get SIGTERM before the loop exited")
	}
	if exists(filepath.Join(e.cfg.WorkspaceRoot, id+"-one", stateDir, "pid")) {
		t.Fatal("pid file left")
	}
}

// Acceptance 9: tix missing or failing skips the tick and does not stop the runs.
func TestTrackerErrorLeavesRunsAlone(t *testing.T) {
	for name, tr := range map[string]Tracker{
		"bad dir":     &Tix{Dir: "/nonexistent-dir-for-test", Timeout: time.Second},
		"tix missing": &Tix{Dir: t.TempDir(), Timeout: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "tix missing" {
				t.Setenv("PATH", "")
			}
			stopped := false
			s := newSched(&Config{MaxAgents: 2, Watch: []string{"open"}}, tr, slog.New(slog.DiscardHandler))
			s.running["T-1"] = &active{stop: func(error) { stopped = true }}
			s.tick(t.Context())
			if stopped || len(s.running) != 1 || s.failed != 1 {
				t.Fatalf("stopped %v, running %d, failed %d", stopped, len(s.running), s.failed)
			}
		})
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
