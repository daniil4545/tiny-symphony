package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// stubAgent plays the agent of the prompt in the mode STUB_MODE. It finds its task and actor from the worktree name.
const stubAgent = `#!/bin/sh
id=$(basename "$PWD" | sed 's/^\(T-[0-9]*\)-.*/\1/')
a="tiny-symphony:stub:$id"
printf '%s' "$1" > .tiny-symphony/prompt
pwd -P > .tiny-symphony/cwd
echo "$id" >> "$STUB_DIR/starts"
trap 'touch "$STUB_DIR/term-$id"; exit 0' TERM
wait_for() { while [ ! -f "$STUB_DIR/$1" ]; do sleep 0.05 & wait $!; done; }
case "$STUB_MODE" in
handoff)
	tix take "$id" --as "$a" && tix set "$id" external=http://pr/1 --as "$a" &&
		tix move "$id" blocked -m "Ждёт: review PR" --as "$a"
	exit "${STUB_EXIT:-0}" ;;
noop) exit 0 ;;
crash) tix take "$id" --as "$a"; exit 1 ;;
noas) tix take "$id"; exit 1 ;;
hold) tix take "$id" --as "$a"; touch "$STUB_DIR/taken-$id"; sleep 60 & wait $! ;;
release)
	tix take "$id" --as "$a"; touch "$STUB_DIR/taken-$id"; wait_for go
	tix take "$id" --release --as "$a"; touch "$STUB_DIR/released-$id"; sleep 60 & wait $! ;;
esac
`

type env struct {
	cfg  *Config
	tx   *Tix // the helper hand of the tests, writes as the tiny-symphony actor too
	repo string
	dir  string // STUB_DIR
	home string // TIX_HOME
}

// newEnv makes a temp tix store with the project toy, a toy repo and a config with a stub agent. Real tix and git run.
func newEnv(t *testing.T, mode string) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("git is required on PATH: %v", err)
	}
	e := &env{tx: tixEnv(t), repo: toyRepo(t), dir: t.TempDir()}
	e.home = os.Getenv("TIX_HOME")
	tixDo(t, e.tx, "", "projects", "add", "toy", e.repo)
	tixDo(t, e.tx, "", "projects", "add", "nopath")
	script := filepath.Join(t.TempDir(), "agent.sh")
	write(t, script, stubAgent, 0o700)
	t.Setenv("STUB_DIR", e.dir)
	t.Setenv("STUB_MODE", mode)
	e.cfg = &Config{
		Tracker: "tix", WorkspaceRoot: t.TempDir(), Watch: []string{"open"},
		HandoffStatus: "blocked", BlockedStatus: "blocked", ReopenStatus: "open",
		RunTimeoutMinutes: 1, PollSeconds: 1, MaxAgents: 2, MaxAttempts: 3, Cleanup: Cleanup{LogDays: 14},
		Agents: map[string]Agent{"stub": {Cmd: []string{script, "{prompt}"}}},
	}
	return e
}

func (e *env) sched() *sched {
	s := newSched(e.cfg, newTracker(e.cfg, e.tx.Dir), slog.New(slog.DiscardHandler))
	s.backoff = []time.Duration{time.Millisecond}
	s.poll, s.grace, s.trackerPause = 50*time.Millisecond, time.Second, 0
	return s
}

func (e *env) run(t *testing.T) int { t.Helper(); return e.sched().loop(t.Context(), true) }

var idRe = regexp.MustCompile(`T-\d+`)

// create makes a task with one criterion in open (the watched status) and returns its id.
func (e *env) create(t *testing.T, title string, tags ...string) string {
	t.Helper()
	return e.createIn(t, title, "toy", "", tags...)
}

func (e *env) createIn(t *testing.T, title, project, body string, tags ...string) string {
	t.Helper()
	out, err := e.tx.run(t.Context(), "new", title, "--project", project)
	if err != nil {
		t.Fatal(err)
	}
	id := idRe.FindString(string(out))
	raw, err := e.tx.run(t.Context(), "show", id, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var j struct {
		Rev string `json:"rev"`
	}
	if err = json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	tixDo(t, e.tx, "## Details\n- [ ] a\n"+body, "edit", id, "--base", j.Rev)
	if len(tags) > 0 {
		tixDo(t, e.tx, "", "set", id, "tags="+strings.Join(tags, ","))
	}
	tixDo(t, e.tx, "", "move", id, "open")
	return id
}

func (e *env) get(t *testing.T, id string) TrackerTask {
	t.Helper()
	task, err := e.tx.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// daemon lists the subjects of the store commits written by the daemon's own actor.
func (e *env) daemon(t *testing.T) []string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", e.home, "log", "--format=%s", "-E", "--grep=^Actor: tiny-symphony$").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func (e *env) starts(t *testing.T) []string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(e.dir, "starts"))
	return strings.Fields(string(data))
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

// spec: R2, R5
// Acceptance 1: the agent takes, hands over; the run is a success and the daemon writes nothing.
func TestHappyPathDaemonWritesNothing(t *testing.T) {
	e := newEnv(t, "handoff")
	mainHead := mustRun(t, e.repo, "rev-parse", "main")
	id := e.create(t, "Write it", "agent:stub")
	other := e.create(t, "Human task", "quick")

	if failed := e.run(t); failed != 0 {
		t.Fatalf("failed = %d", failed)
	}
	got := e.get(t, id)
	if got.Status != "blocked" || got.External != "http://pr/1" || got.Claim != "" {
		t.Fatalf("task = %+v", got)
	}
	if c := e.daemon(t); len(c) != 0 {
		t.Fatalf("the daemon wrote: %v", c)
	}
	if mustRun(t, e.repo, "rev-parse", "main") != mainHead || mustRun(t, e.repo, "remote") != "" {
		t.Fatal("the daemon must not merge or push")
	}
	if e.get(t, other).Status != "open" || len(e.starts(t)) != 1 {
		t.Fatal("a task without agent:<name> must not be touched")
	}
	wt := filepath.Join(e.cfg.WorkspaceRoot, id+"-write-it")
	prompt, _ := os.ReadFile(filepath.Join(wt, stateDir, "prompt"))
	for _, want := range []string{"--as tiny-symphony:stub:" + id, "tix move " + id + " blocked", wt} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if log, _ := exec.CommandContext(t.Context(), "git", "-C", e.home, "log", "--format=%b").Output(); !strings.Contains(string(log), "Actor: tiny-symphony:stub:"+id) {
		t.Fatalf("the claim does not carry the run actor:\n%s", log)
	}
	// the task left the watched statuses, so a second tick finds nothing
	if e.run(t); len(e.starts(t)) != 1 {
		t.Fatal("the task was run twice")
	}
}

// spec: S5, S4
// Acceptance 2: exit 0 without moving the task is a failed attempt; max_attempts give a note and Block, the count resets.
func TestNoHandoverRetriesThenBlocks(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "Lazy", "agent:stub")
	s := e.sched()

	drive(t, s, 3)
	got := e.get(t, id)
	if len(e.starts(t)) != 3 || got.Status != "blocked" || s.failed != 3 {
		t.Fatalf("starts %v, task %+v, failed %d", e.starts(t), got, s.failed)
	}
	c := e.daemon(t)
	if len(c) != 2 || !strings.Contains(strings.Join(c, " "), "note") || !strings.Contains(c[0], "blocked") {
		t.Fatalf("the daemon wrote %v, want a note and a move to blocked", c)
	}
	if exists(filepath.Join(e.cfg.WorkspaceRoot, id+"-lazy", stateDir, "attempts")) || s.tries[id] != nil {
		t.Fatal("the count must be reset by Block")
	}
	if note := lastNote(t, e, id); !strings.Contains(note, "gave up after 3") || !strings.Contains(note, ".log") {
		t.Fatalf("note = %q", note)
	}

	// a human moves it back: a fresh limit, one failed attempt counted
	tixDo(t, e.tx, "", "move", id, "open", "--as", "human:1")
	drive(t, s, 1)
	if len(e.starts(t)) != 4 || s.tries[id] == nil || s.tries[id].N != 1 {
		t.Fatalf("starts %v, tries %+v", e.starts(t), s.tries[id])
	}
}

func lastNote(t *testing.T, e *env, id string) string {
	t.Helper()
	out, err := e.tx.run(t.Context(), "show", id)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// spec: S1
// Acceptance 3: the agent takes and crashes: note, back to reopen_status, retried; the limit still holds.
func TestCrashAfterTakeReopensAndRetries(t *testing.T) {
	e := newEnv(t, "crash")
	id := e.create(t, "Crashy", "agent:stub")
	s := e.sched()

	drive(t, s, 1)
	got := e.get(t, id)
	if got.Status != "open" || got.Claim != "" || len(e.daemon(t)) != 2 {
		t.Fatalf("task %+v, daemon %v", got, e.daemon(t))
	}
	if note := lastNote(t, e, id); !strings.Contains(note, "Back to open") || !strings.Contains(note, ".log") {
		t.Fatalf("note = %q", note)
	}
	drive(t, s, 2) // a retry with the reason, then the limit: Block after a Reopen
	if got = e.get(t, id); len(e.starts(t)) != 3 || got.Status != "blocked" {
		t.Fatalf("starts %v, task %+v", e.starts(t), got)
	}
}

// Acceptance 4: the agent hands over and exits 1: a success.
func TestHandoverThenExitOneIsSuccess(t *testing.T) {
	t.Setenv("STUB_EXIT", "1")
	e := newEnv(t, "handoff")
	id := e.create(t, "Noisy", "agent:stub")
	s := e.sched()
	drive(t, s, 1)
	if s.failed != 0 || e.get(t, id).Status != "blocked" || len(e.daemon(t)) != 0 || s.tries[id] != nil {
		t.Fatalf("failed %d, task %+v, daemon %v", s.failed, e.get(t, id), e.daemon(t))
	}
}

// Acceptance 5: a human moved the task between the deciding read and the write: nothing is written.
func TestWriteIsGuardedByTheDecidingRead(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "Raced", "agent:stub")
	s := e.sched()
	tixDo(t, e.tx, "", "take", id, "--as", "tiny-symphony:stub:"+id)
	snap := e.get(t, id) // the deciding read: in_progress with our claim
	tixDo(t, e.tx, "", "move", id, "blocked", "-m", "human needs it", "--as", "human:1")

	_, err := s.judge(t.Context(), snap, "tiny-symphony:stub:"+id, nil, "x.log", s.log)
	if !errors.Is(err, errMoved) || len(e.daemon(t)) != 0 {
		t.Fatalf("err %v, daemon wrote %v", err, e.daemon(t))
	}
	// the same for Block
	snap.Claim = "" // as after a Reopen
	s.tries[id] = &tries{N: 3, Last: "boom", snap: snap}
	s.block(t.Context(), id)
	if len(e.daemon(t)) != 0 || s.tries[id] != nil || e.get(t, id).Status != "blocked" {
		t.Fatalf("daemon wrote %v", e.daemon(t))
	}
}

// spec: S3
// Acceptance 6: a run is stopped, with no attempt but with a pause, when the task changed hands.
func TestRunStoppedWhenTaskChangesHands(t *testing.T) {
	cases := map[string]func(t *testing.T, e *env, id string){
		"tag removed": func(t *testing.T, e *env, id string) { t.Helper(); tixDo(t, e.tx, "", "set", id, "tags=x") },
		"moved back to open": func(t *testing.T, e *env, id string) {
			t.Helper()
			tixDo(t, e.tx, "", "move", id, "open", "--as", "human:1")
		},
		"claim stolen": func(t *testing.T, e *env, id string) {
			t.Helper()
			tixDo(t, e.tx, "", "take", id, "--steal", "--as", "human:1")
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "hold")
			id := e.create(t, "Held", "agent:stub")
			s := e.sched()
			s.backoff = []time.Duration{time.Hour}

			s.tick(t.Context())
			waitFor(t, "the claim", func() bool { return exists(filepath.Join(e.dir, "taken-"+id)) })
			s.tick(t.Context()) // reconcile sees our claim
			change(t, e, id)
			s.tick(t.Context())
			s.next()

			if !exists(filepath.Join(e.dir, "term-"+id)) || s.failed != 0 || len(e.daemon(t)) != 0 {
				t.Fatalf("term %v, failed %d, daemon %v", exists(filepath.Join(e.dir, "term-"+id)), s.failed, e.daemon(t))
			}
			if tr := s.tries[id]; tr == nil || tr.N != 0 || !tr.next.After(time.Now().Add(time.Minute)) {
				t.Fatalf("tries = %+v, want no attempt and a pause", tr)
			}
			s.tick(t.Context()) // inside the pause nothing starts, even if the task is open and tagged
			if len(s.running) != 0 || len(e.starts(t)) != 1 {
				t.Fatalf("running %d, starts %v", len(s.running), e.starts(t))
			}
		})
	}
}

// The agent releases the task itself and sleeps: stopped the same way.
func TestReleasingAgentIsStopped(t *testing.T) {
	e := newEnv(t, "release")
	id := e.create(t, "Releasing", "agent:stub")
	s := e.sched()
	s.backoff = []time.Duration{time.Hour}

	s.tick(t.Context())
	waitFor(t, "the claim", func() bool { return exists(filepath.Join(e.dir, "taken-"+id)) })
	s.tick(t.Context()) // reconcile sees our claim
	write(t, filepath.Join(e.dir, "go"), "", 0o600)
	waitFor(t, "the release", func() bool { return exists(filepath.Join(e.dir, "released-"+id)) })
	s.tick(t.Context())
	s.next()
	s.tick(t.Context())
	if !exists(filepath.Join(e.dir, "term-"+id)) || s.failed != 0 || len(e.starts(t)) != 1 || len(s.running) != 0 {
		t.Fatalf("failed %d, starts %v, running %d", s.failed, e.starts(t), len(s.running))
	}
}

// Acceptance 6a: an agent without --as crashes: the task stays in_progress under a foreign claim, one note, no move.
func TestForeignClaimGetsOneNoteNoMove(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess1")
	e := newEnv(t, "noas")
	id := e.create(t, "No as", "agent:stub")
	s := e.sched()

	drive(t, s, 1)
	got := e.get(t, id)
	if got.Status != "in_progress" || got.Claim != "claude:sess1" || len(e.daemon(t)) != 1 {
		t.Fatalf("task %+v, daemon %v", got, e.daemon(t))
	}
	if note := lastNote(t, e, id); !strings.Contains(note, "left in_progress by claude:sess1") {
		t.Fatalf("note = %q", note)
	}
	drive(t, s, 2) // the task is not in a watched status: no second run, no second note
	if len(e.daemon(t)) != 1 || len(e.starts(t)) != 1 || s.tries[id].N != 1 {
		t.Fatalf("daemon %v, starts %v", e.daemon(t), e.starts(t))
	}
}

// Collect: tag of a known agent, no live claim, not an epic, After: tasks closed, a project with a repository.
func TestCollectFilters(t *testing.T) {
	e := newEnv(t, "handoff")
	e.create(t, "Epic", "agent:stub", "epic")
	e.create(t, "Unknown agent", "agent:nobody")
	e.createIn(t, "No repo", "nopath", "", "agent:stub")
	held := e.create(t, "Held by a human", "agent:stub")
	tixDo(t, e.tx, "", "take", held, "--as", "human:1")
	tixDo(t, e.tx, "", "move", held, "open", "--as", "human:1") // back to open: no claim, so it is collected
	dep := e.create(t, "Dependency")
	waits := e.createIn(t, "Waits", "toy", "After: "+dep+"\n", "agent:stub")
	s := e.sched()

	drive(t, s, 1)
	if got := e.starts(t); len(got) != 1 || got[0] != held {
		t.Fatalf("starts = %v, want only %s", got, held)
	}
	tixDo(t, e.tx, "", "check", dep, "1")
	tixDo(t, e.tx, "", "done", dep, "-m", "r")
	drive(t, s, 1)
	if got := e.starts(t); len(got) != 2 || got[1] != waits {
		t.Fatalf("starts = %v, want %s after its dependency closed", got, waits)
	}
}

// Collect order: rank, then oldest, then id.
func TestCollectOrder(t *testing.T) {
	e := newEnv(t, "handoff")
	e.cfg.MaxAgents = 1
	first := e.create(t, "one", "agent:stub")
	second := e.create(t, "two", "agent:stub")
	third := e.create(t, "three", "agent:stub")
	tixDo(t, e.tx, "", "move", third, "open", "--first")
	drive(t, e.sched(), 3)
	if got := strings.Join(e.starts(t), " "); got != fmt.Sprint(third, " ", first, " ", second) {
		t.Fatalf("starts = %s", got)
	}
}

func TestLive(t *testing.T) {
	now := time.Now()
	f := "2006-01-02T15:04"
	for claim, want := range map[string][2]string{"live": {"a:b", now.Add(time.Hour).Format(f)}, "expired": {"a:b", now.Add(-time.Hour).Format(f)}, "none": {"", ""}, "no expiry": {"a:b", ""}} {
		got := live(TrackerTask{Claim: want[0], ClaimUntil: want[1]})
		if got != (claim == "live" || claim == "no expiry") {
			t.Errorf("%s: live = %v", claim, got)
		}
	}
}

// spec: R2
// The tag was removed after the list: the run is skipped before a worktree is made.
func TestRunSkippedWhenTagRemovedSinceTheList(t *testing.T) {
	e := newEnv(t, "handoff")
	id := e.create(t, "Tagged", "agent:stub")
	listed := e.get(t, id)
	tixDo(t, e.tx, "", "set", id, "tags=human")
	s := e.sched()
	if _, err := s.runTask(t.Context(), t.Context(), listed, "stub", e.cfg.Agents["stub"], e.repo, 1, "", s.log); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(e.cfg.WorkspaceRoot); len(entries) != 0 || len(e.starts(t)) != 0 {
		t.Fatalf("a workspace was made for a task without the tag: %v", entries)
	}
}

// spec: R2
// The tag was removed during the run: the daemon writes nothing, not even a note.
func TestNoWriteWhenTagRemovedDuringTheRun(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "Untagged", "agent:stub")
	s := e.sched()
	name := "tiny-symphony:stub:" + id
	tixDo(t, e.tx, "", "take", id, "--as", name)
	snap := e.get(t, id) // in_progress, our claim, still tagged
	tixDo(t, e.tx, "", "set", id, "tags=human")
	if _, err := s.judge(t.Context(), snap, name, nil, "x.log", s.log); !errors.Is(err, errMoved) || len(e.daemon(t)) != 0 {
		t.Fatalf("err %v, daemon wrote %v", err, e.daemon(t))
	}
}

// Block does not move a task that someone holds.
func TestBlockSkipsALiveForeignClaim(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "Held", "agent:stub")
	s := e.sched()
	tixDo(t, e.tx, "", "take", id, "--as", "human:1")
	s.tries[id] = &tries{N: 3, Last: "boom", snap: e.get(t, id)}
	s.block(t.Context(), id)
	if len(e.daemon(t)) != 0 || e.get(t, id).Status != "in_progress" {
		t.Fatalf("daemon wrote %v", e.daemon(t))
	}
}

// spec: Run
// An agent task left in in_progress with no run: ours (or no claim) is reopened as a failed attempt, until Block.
func TestOrphanInProgressIsReopened(t *testing.T) {
	e := newEnv(t, "noop")
	ours := e.create(t, "Ours", "agent:stub")
	bare := e.create(t, "Bare", "agent:stub")
	tixDo(t, e.tx, "", "take", ours, "--as", actorName("stub", ours))
	tixDo(t, e.tx, "", "move", bare, "in_progress")
	s := e.sched()

	s.recoverOrphans(t.Context(), map[string]string{"toy": e.repo})
	for _, id := range []string{ours, bare} {
		if got := e.get(t, id); got.Status != "open" || got.Claim != "" {
			t.Fatalf("%s: task %+v", id, got)
		}
	}
	if s.failed != 2 || s.tries[bare].N != 1 || s.tries[ours].N != 0 { // own claim: a pause, no attempt
		t.Fatalf("failed %d, tries %+v %+v", s.failed, s.tries[ours], s.tries[bare])
	}
	if note := lastNote(t, e, ours); !strings.Contains(note, "left in_progress with no run. Back to open") {
		t.Fatalf("note = %q", note)
	}
	for range 2 { // the limit still holds for an empty claim
		time.Sleep(2 * time.Millisecond)
		tixDo(t, e.tx, "", "move", bare, "in_progress")
		s.recoverOrphans(t.Context(), map[string]string{"toy": e.repo})
	}
	if got := e.get(t, bare); got.Status != "blocked" {
		t.Fatalf("task %+v", got)
	}
}

// failingReopen is a Tracker whose Reopen fails after the note was written.
type failingReopen struct {
	Tracker
	notes int
}

func (f *failingReopen) Note(ctx context.Context, id, text string) error {
	f.notes++
	return f.Tracker.Note(ctx, id, text)
}
func (f *failingReopen) Reopen(context.Context, string) error { return errors.New("tix down") }

// A failed Reopen after the note must not repeat the note on the next tick.
func TestOrphanReopenFailureWritesOneNote(t *testing.T) {
	e := newEnv(t, "noop")
	id := e.create(t, "Stuck", "agent:stub")
	tixDo(t, e.tx, "", "take", id, "--as", actorName("stub", id))
	s := e.sched()
	s.backoff = []time.Duration{time.Hour}
	f := &failingReopen{Tracker: s.tr}
	s.tr = f

	for range 2 {
		s.recoverOrphans(t.Context(), map[string]string{"toy": e.repo})
	}
	if f.notes != 1 || e.get(t, id).Status != "in_progress" {
		t.Fatalf("notes %d, task %+v", f.notes, e.get(t, id))
	}
}

// spec: R2, R6
// A foreign live claim and a task without agent: are left alone.
func TestOrphanLeavesForeignAndUntagged(t *testing.T) {
	e := newEnv(t, "noop")
	foreign := e.create(t, "Foreign", "agent:stub")
	plain := e.create(t, "Plain")
	tixDo(t, e.tx, "", "take", foreign, "--as", "claude:other")
	tixDo(t, e.tx, "", "move", plain, "in_progress")
	s := e.sched()

	s.recoverOrphans(t.Context(), map[string]string{"toy": e.repo})
	if a, b := e.get(t, foreign), e.get(t, plain); a.Status != "in_progress" || b.Status != "in_progress" || len(e.daemon(t)) != 0 {
		t.Fatalf("foreign %+v, plain %+v, daemon %v", a, b, e.daemon(t))
	}
}
