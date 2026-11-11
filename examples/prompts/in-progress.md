Implement task {{.id}}: {{.title}}

{{.description}}

Spec and plan:
{{.plan}}

Acceptance criteria:
{{range .criteria}}{{.index}}. {{.text}}
{{end}}
Work in this worktree (branch {{.branch}}). Commit your
work, run the checks of the project, and make sure every criterion holds.

Before you exit, write .tiny-symphony/result.json:

{"status": "Review", "summary": "<what was done>", "notes": "<PR link and remarks>", "checked_ac": [1]}

Put the numbers of the criteria that hold into checked_ac. If you are stuck, write
{"status": "Blocked", "comment": "<one question for the human>"}.
{{if .last_error}}
Attempt {{.attempt}}. The previous attempt failed: {{.last_error}}
{{end}}
