Task {{.id}}: {{.title}} came back from review.

Review comments:
{{range .comments}}- {{.}}
{{end}}
Fix what they ask for in this worktree (branch {{.branch}}),
commit, and check the criteria again:
{{range .criteria}}{{.index}}. {{.text}}
{{end}}
Before you exit, write .tiny-symphony/result.json:

{"status": "Review", "summary": "<what was fixed>"}

If you need an answer from the human, write {"status": "Blocked", "comment": "<question>"}.
{{if .last_error}}
Attempt {{.attempt}}. The previous attempt failed: {{.last_error}}
{{end}}
