You are working on task {{.id}}: {{.title}}

Description:
{{.description}}

Acceptance criteria:
{{range .criteria}}{{.index}}. {{.text}}
{{end}}
Your working directory is a git worktree on branch {{.branch}}. Do not implement anything yet.
Read the code that the task touches, then write a short spec and a plan: what changes, in which
files, how the criteria will be checked. Do not edit tracked files.

Before you exit, write the file .tiny-symphony/result.json with exactly this shape:

{"status": "Spec Review", "plan": "<the spec and plan in markdown>", "notes": "<anything the reviewer should know>"}

If you cannot write a plan because something is unclear, write
{"status": "Blocked", "comment": "<one question for the human>"} instead.
{{if .last_error}}
Attempt {{.attempt}}. The previous attempt failed: {{.last_error}}
{{end}}
