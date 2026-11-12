package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// tixEnv gives a fresh tix store in a temp TIX_HOME and an adapter over it.
// The tests run the real tix; a missing binary fails them (CI installs it).
func tixEnv(t *testing.T) *Tix {
	t.Helper()
	if _, err := exec.LookPath("tix"); err != nil {
		t.Fatalf("tix is required on PATH: %v", err)
	}
	home := t.TempDir()
	t.Setenv("TIX_HOME", home)
	return &Tix{Dir: t.TempDir(), Actor: "tiny-symphony", Blocked: "blocked", Reopened: "open", Timeout: 10 * time.Second}
}

func tixDo(t *testing.T, x *Tix, stdin string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "tix", args...)
	cmd.Dir = x.Dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tix %v: %v: %s", args, err, out)
	}
}

func TestTixRoundTrip(t *testing.T) {
	x := tixEnv(t)
	ctx := t.Context()
	repo := t.TempDir()
	tixDo(t, x, "", "projects", "add", "demo", repo)
	tixDo(t, x, "", "projects", "add", "nopath")

	repos, err := x.Repos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos["demo"] != repo {
		t.Fatalf("repos = %v, want only demo=%s", repos, repo)
	}

	tixDo(t, x, "", "new", "first", "--project", "demo")
	raw, err := x.run(ctx, "show", "T-1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	rev := raw[strings.Index(string(raw), `"rev":"`)+7:]
	rev = rev[:strings.Index(string(rev), `"`)]
	tixDo(t, x, "## Details\n- [ ] a\nAfter: T-2, T-3\n", "edit", "T-1", "--base", string(rev))
	tixDo(t, x, "", "set", "T-1", "tags=agent:claude,x", "external=http://pr/1")
	tixDo(t, x, "", "move", "T-1", "open")

	open, err := x.List(ctx, []string{"open", "in_progress"})
	if err != nil || len(open) != 1 {
		t.Fatalf("list = %v, %v", open, err)
	}
	got := open[0]
	if got.ID != "T-1" || got.Status != "open" || got.Project != "demo" || got.External != "http://pr/1" ||
		got.Rank != 1 || len(got.Tags) != 2 || got.Tags[0] != "agent:claude" || got.Created.IsZero() || got.Claim != "" {
		t.Fatalf("task = %+v", got)
	}

	tixDo(t, x, "", "take", "T-1", "--as", "me:1")
	g, err := x.Get(ctx, "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "in_progress" || g.Claim != "me:1" || g.ClaimUntil == "" || strings.Join(g.After, " ") != "T-2 T-3" {
		t.Fatalf("get = %+v", g)
	}

	long := strings.Repeat("ж", 500)
	if err := x.Note(ctx, "T-1", "-"+long); err != nil {
		t.Fatalf("note starting with a dash and over the limit: %v", err)
	}
	if err := x.Block(ctx, "T-1", "gave up"); err != nil {
		t.Fatal(err)
	}
	if g, _ = x.Get(ctx, "T-1"); g.Status != "blocked" || g.Claim != "" {
		t.Fatalf("after block = %+v", g)
	}
	if err := x.Reopen(ctx, "T-1"); err != nil {
		t.Fatal(err)
	}
	if g, _ = x.Get(ctx, "T-1"); g.Status != "open" {
		t.Fatalf("after reopen = %+v", g)
	}
}

func TestTixErrors(t *testing.T) {
	x := tixEnv(t)
	_, err := x.Get(t.Context(), "T-99")
	if !errors.Is(err, errTracker) || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
	x.Dir = "/nonexistent-dir-for-test"
	if _, err := x.List(t.Context(), []string{"open"}); !errors.Is(err, errTracker) {
		t.Fatalf("bad dir err = %v", err)
	}
}

func TestTixTimeout(t *testing.T) {
	x := tixEnv(t)
	x.Timeout = time.Nanosecond
	start := time.Now()
	if _, err := x.Get(t.Context(), "T-1"); !errors.Is(err, errTracker) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestTixTaskParse(t *testing.T) {
	j := tixJSON{
		ID: "T-5", Status: " In_Progress ", Claim: "a:b until 2026-01-02T03:04", Created: "2026-01-02T03:04:05",
		Body: "x\nAfter: T-1\nAfter: T-2 T-3\nnot After: T-9\n",
	}
	if err := json.Unmarshal([]byte(`{"rank":1.5}`), &j); err != nil || j.Rank != 1.5 {
		t.Fatalf("fractional rank: %v, %v", j.Rank, err)
	}
	j.Rank = 0
	got, err := j.task()
	if err != nil || got.Status != "in_progress" || got.Claim != "a:b" || got.ClaimUntil != "2026-01-02T03:04" ||
		strings.Join(got.After, ",") != "T-1,T-2,T-3" || got.Created.Year() != 2026 {
		t.Fatalf("got %+v, %v", got, err)
	}
}
