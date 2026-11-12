package main

import (
	"strings"
	"testing"
)

func promptVars(attempt int, lastErr string) map[string]any {
	return map[string]any{
		"id": "T-7", "title": "Do it", "branch": "T-7-do-it", "worktree": "/w/T-7-do-it",
		"actor": "tiny-symphony:claude:T-7", "handoff_status": "review", "attempt": attempt, "last_error": lastErr,
	}
}

func TestPromptHasTheActorTheHandoverAndTheBans(t *testing.T) {
	got, err := renderPrompt(promptVars(1, ""), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"T-7: Do it", "--as tiny-symphony:claude:T-7", "tix take T-7", "/w/T-7-do-it", "T-7-do-it",
		`tix move T-7 review -m "Ждёт: review PR"`, "external=<PR url>",
		"no tix done", "no tix take --release", "no --steal", "tag agent:",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(got, "attempt") || strings.Contains(got, "<no value>") {
		t.Errorf("a first attempt has no retry line:\n%s", got)
	}
}

func TestPromptRetryLineAndAppend(t *testing.T) {
	got, err := renderPrompt(promptVars(2, "agent exited: exit status 1; log x.log"), "Use make check.")
	if err != nil || !strings.Contains(got, "attempt 2") || !strings.Contains(got, "exit status 1; log x.log") ||
		!strings.HasSuffix(got, "\nUse make check.\n") {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPromptIsStrict(t *testing.T) {
	vars := promptVars(1, "")
	delete(vars, "actor")
	if _, err := renderPrompt(vars, ""); err == nil {
		t.Fatal("a missing variable must fail the render")
	}
}
