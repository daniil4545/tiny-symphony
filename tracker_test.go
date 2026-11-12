package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const listJSON = `{"schemaVersion":1,"kind":"task-list","tasks":[
 {"id":"T-10","title":"late high","status":"To Do","priority":"high","labels":["agent:claude"],"createdAt":"2026-11-02T10:00:00Z","isReady":true},
 {"id":"T-2","title":"early low","status":" to do ","priority":"low","labels":["Agent:Claude "],"createdAt":"2026-11-01T10:00:00Z","isReady":true},
 {"id":"T-9","title":"same day as ten","status":"To Do","priority":"high","labels":["agent:claude"],"createdAt":"2026-11-02T10:00:00Z","isReady":true},
 {"id":"T-3","title":"no label","status":"To Do","priority":"high","labels":["quick"],"createdAt":"2026-11-01T10:00:00Z","isReady":true},
 {"id":"T-4","title":"human gate","status":"Spec Review","priority":"high","labels":["agent:claude"],"createdAt":"2026-11-01T10:00:00Z","isReady":true},
 {"id":"T-5","title":"deps open","status":"To Do","priority":"high","labels":["agent:claude"],"createdAt":"2026-11-01T10:00:00Z","isReady":false},
 {"id":"T-6","title":"no priority","status":"Rework","labels":["agent:codex"],"createdAt":"2026-11-01T10:00:00Z","isReady":true}
]}`

func ids(tasks []Task) []string {
	var out []string
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func TestParseListSelectsAndOrders(t *testing.T) {
	tasks, err := parseList([]byte(listJSON))
	if err != nil {
		t.Fatal(err)
	}
	// Skipped: no agent label, a human status, open dependencies.
	want := []string{"T-9", "T-10", "T-2", "T-6"}
	if got := ids(tasks); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if tasks[2].agentName() != "claude" {
		t.Fatalf("agent = %q, labels compare trimmed and lowercased", tasks[2].agentName())
	}
}

func TestParseListBadJSON(t *testing.T) {
	if _, err := parseList([]byte("not json")); err == nil {
		t.Fatal("want an error")
	}
}

// spec: R3, R4
func TestCheckResult(t *testing.T) {
	todo := Task{Status: "To Do", Criteria: []Criterion{{Index: 1}, {Index: 2}}}
	quick := Task{Status: "To Do", Labels: []string{"quick"}}
	cases := []struct {
		name string
		task Task
		res  Result
		want string
		bad  bool
	}{
		{"spec", todo, Result{Status: "spec review", Plan: "p", CheckedAC: []int{1, 2}}, "Spec Review", false},
		{"spec needs a plan", todo, Result{Status: "Spec Review"}, "", true},
		{"blocked needs a comment", todo, Result{Status: "Blocked"}, "", true},
		{"blocked", todo, Result{Status: "Blocked", Comment: "why?"}, "Blocked", false},
		{"done is not a transition", todo, Result{Status: "Done"}, "", true},
		{"in progress is a human step", todo, Result{Status: "In Progress"}, "", true},
		{"review needs quick", todo, Result{Status: "Review"}, "", true},
		{"review with quick", quick, Result{Status: "Review"}, "Review", false},
		{"human status has no transitions", Task{Status: "Spec Review"}, Result{Status: "In Progress"}, "", true},
		{"criterion out of range", todo, Result{Status: "Spec Review", Plan: "p", CheckedAC: []int{3}}, "", true},
		{"rework to review", Task{Status: "Rework"}, Result{Status: "Review"}, "Review", false},
	}
	for _, c := range cases {
		got, err := checkResult(c.res, c.task)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
	}
}

func TestEditArgsKeepDashes(t *testing.T) {
	got := editArgs("T-1", Result{Plan: "- step", Notes: "n", CheckedAC: []int{2}})
	want := []string{"task", "edit", "T-1", "--plan=- step", "--append-notes=n", "--check-ac=2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestApplyWritesStatusLast(t *testing.T) {
	var calls []string
	bl := &Backlog{run: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return nil, nil
	}}
	if err := bl.Apply(t.Context(), "T-1", Result{Plan: "p"}, "Spec Review"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != "task edit T-1 --plan=p --status=Spec Review" {
		t.Fatalf("calls = %q", calls)
	}
}

func TestHungBacklogTimesOut(t *testing.T) {
	bin := t.TempDir()
	write(t, filepath.Join(bin, "backlog"), "#!/bin/sh\nexec sleep 30\n", 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := backlogTimeout
	backlogTimeout = 200 * time.Millisecond
	t.Cleanup(func() { backlogTimeout = old })
	start := time.Now()
	_, err := NewBacklog(t.TempDir()).List(t.Context())
	if !errors.Is(err, errTracker) || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}

func TestLoadParsesView(t *testing.T) {
	view := `{"task":{"id":"T-1","title":"x","status":"To Do","labels":["agent:claude"],"project":"toy",
	"description":"d","implementationPlan":null,"comments":[{"body":"c1"}],
	"acceptanceCriteria":[{"index":1,"text":"a","checked":true}]}}`
	bl := &Backlog{run: func(context.Context, ...string) ([]byte, error) { return []byte(view), nil }}
	task, err := bl.Load(t.Context(), "T-1")
	if err != nil {
		t.Fatal(err)
	}
	if task.Description != "d" || task.Project != "toy" || task.Comments[0] != "c1" || !task.Criteria[0].Checked {
		t.Fatalf("task = %+v", task)
	}
}
