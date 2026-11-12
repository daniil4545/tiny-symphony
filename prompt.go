package main

import (
	"fmt"
	"strings"
	"text/template"
)

// promptText is the one prompt for every run. Project specifics come from the agent's own
// rules files and prompt_append; the task text is read by the agent from the tracker.
const promptText = `You work on tix task {{.id}}: {{.title}}.
Run every tix command with --as {{.actor}}.

1. Take the task: tix take {{.id}} --as {{.actor}}. Read it with tix show {{.id}}: the criteria, the notes and the comments of the human.
2. Work only in this worktree: {{.worktree}} (branch {{.branch}}). Not in the project path that tix take prints.
3. Run the project checks, commit, push the branch and open a PR.
4. Put the PR link into the task: tix set {{.id}} external=<PR url>. Write a note with what you did.
5. Hand the task over: tix move {{.id}} {{.handoff_status}} -m "Ждёт: review PR".

Do not:
- close the task: no tix done. A human closes it after the review.
- release the task when the work fails: no tix take --release. Write a note and exit, the daemon returns the task.
- steal the task when take is refused: no --steal. Just exit.
- add a tag agent: to new tasks. Only a human adds it.
{{if gt .attempt 1}}
This is attempt {{.attempt}}. The previous one failed: {{.last_error}}
{{end}}`

// renderPrompt fills the template strictly: an unknown variable fails the run.
func renderPrompt(vars map[string]any, extra string) (string, error) {
	tpl, err := template.New("prompt").Option("missingkey=error").Parse(promptText)
	if err != nil {
		return "", fmt.Errorf("parse prompt: %w", err)
	}
	var sb strings.Builder
	if err = tpl.Execute(&sb, vars); err != nil {
		return "", fmt.Errorf("render prompt: %w", err)
	}
	if extra = strings.TrimSpace(extra); extra != "" {
		sb.WriteString("\n" + extra + "\n")
	}
	return sb.String(), nil
}
