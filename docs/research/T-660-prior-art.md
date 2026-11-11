# T-660 Prior art

Checked 2026-11-11. Repositories were cloned and read; none was run.

## OpenAI Symphony

[openai/symphony](https://github.com/openai/symphony), [SPEC.md](https://github.com/openai/symphony/blob/main/SPEC.md)
at `be10a1b` (2312 lines), reference implementation in Elixir.

- A long-running service: polls the tracker, makes a workspace per issue, runs a coding agent in it
  (SPEC 1). Policy and prompt live in a repo-owned `WORKFLOW.md` (SPEC 5).
- The service is "a scheduler/runner and tracker reader"; ticket writes are done by the agent
  through tools the orchestrator serves, so the agent never sees the tracker token (SPEC 1, 11.5).
- A run stops when its issue leaves the active states; failures retry with
  `min(10000 * 2^(attempt-1), 300000)` ms (SPEC 8.4, 8.5). No database: restart recovers from the
  tracker and the filesystem (SPEC 2.1).
- Workspace safety: agent cwd must be the issue workspace, inside the root, with a sanitized key
  (SPEC 9.5). Sandboxing and approvals are left to the implementation (SPEC 2.2).
- The agent runner is Codex app-server only (SPEC 10). OpenAI recommends building your own
  hardened version from SPEC.md rather than running theirs (elixir/README.md).

### What to take from Symphony

Proposed, waiting for the user's decision. "In spec" means `docs/spec.md` already has it.

| Idea | Where | Proposal |
| --- | --- | --- |
| Poll, reconcile, dispatch; bounded concurrency | SPEC 8.1-8.3 | in spec |
| Workspace per issue, reused; removed on terminal state, also at startup | SPEC 9.1, 8.6 | in spec |
| Stop a run when its issue leaves the state; retry with backoff | SPEC 8.4-8.5 | in spec |
| No database, restart recovers from the tracker | SPEC 14.3 | in spec |
| Workspace invariants: cwd check, path under root, sanitized key | SPEC 9.5 | in spec as Boundaries |
| Explicit trust boundary | SPEC 15.1 | in spec |
| Normalized issue: id, identifier, title, description, priority, state, labels lowercased, `blocked_by`, `dispatchable`, `created_at` | SPEC 4.1.1 | take: the one seam to the tracker; Backlog.md `isReady` gives `dispatchable`, so tasks with unfinished dependencies wait |
| States compared trimmed and lowercased | SPEC 4.2 | take |
| Candidate order: priority, then oldest, then identifier | SPEC 8.2 | take |
| Strict prompt template: unknown variable fails the run; `attempt` variable | SPEC 5.4, 12.3 | take with Go `text/template` and `missingkey=error`; add `last_error` (SPEC allows `retry_kind`) |
| One owner of scheduler state; workers report back; claim states Unclaimed, Running, RetryQueued, Released | SPEC 7.1, 4.1.8 | take: one goroutine owns the maps, workers send results on a channel |
| Reference algorithms for startup, tick, reconcile, dispatch, worker, exit | SPEC 16 | take as the skeleton of S1-S3 |
| Failure classes: config error skips dispatch but keeps reconcile; tracker fetch error skips the tick; refresh error keeps workers | SPEC 14.1-14.2, 6.3 | take |
| Config reload | SPEC 6.2 (required there) | take cheap: re-read the config when its mtime changes at a tick, keep the last good one on error; no file watcher |
| `~` expansion for path fields only | SPEC 6.1 | take |
| `after_create` hook | SPEC 9.4 | take as one per-project `setup` command (install deps in a new worktree); other hooks no |
| Logs `key=value` with issue id and outcome | SPEC 13.1 | take with `log/slog` text handler |
| One persistent workpad per issue instead of many comments | elixir/WORKFLOW.md | take in Backlog.md terms: the result maps to fields, not comments (`plan` to `--plan`, `notes` to `--append-notes`, `summary` to `--final-summary`, `checked_ac` to `--check-ac`); `comment` only for a question to the human |
| Ticket acceptance criteria are non-negotiable, run before handoff | elixir/WORKFLOW.md | take into the prompts; Backlog.md tasks have AC |
| Out-of-scope findings become new issues, not scope creep | elixir/WORKFLOW.md | take: result field `followups`, the daemon creates drafts |
| Completion bar before Human Review | elixir/WORKFLOW.md | take: the In Progress prompt lists it; the daemon checks what it can (Supervision) |
| Test matrix and real integration profile, skipped means skipped | SPEC 17, 17.8 | take as the test plan; real CLI smoke tests per agent |
| `WORKFLOW.md` in each repo | SPEC 5 | no: one backlog serves many repos, config stays central |
| Merging state where the agent lands the PR | elixir/WORKFLOW.md | later, if merging by hand gets tedious |
| Rework as a full reset: close PR, fresh branch | elixir/WORKFLOW.md | no for v0: incremental fixes on the same branch |
| Multi-turn sessions, continuation retries | SPEC 7.1 | no: one run per status |
| Stall detection, token totals, rate limits from agent events | SPEC 8.5, 13.5 | no: needs the agent's protocol; we only have exit codes |
| HTTP API, dashboard, SSH workers, per-state limits | SPEC 13.7, A, 8.3 | no |

## Ports considered

| Project | Size | Why not |
| --- | --- | --- |
| [sortie-ai/sortie](https://github.com/sortie-ai/sortie) | Go, Apache-2.0, 150 908 lines without tests at `5e8c84c`; nearly all commits by one author | we would use about 5% of it; tracker adapter is 12 methods and the set is closed; one repo and one SCM provider per workflow (`docs/workflow-reference.md:307,688`) |
| [cskwork/oh-my-symphony](https://github.com/cskwork/oh-my-symphony) | Python, Apache-2.0, 84 046 lines at `c062e9a`, one author, 40 stars | its own board in each repo (`kanban/*.md`); default pipeline is autonomous with Human Review only for blockers; factory, PM, chat and browser gates on top |
| [junhoyeo/contrabass](https://github.com/junhoyeo/contrabass) | Go, Apache-2.0 | Claude only through oh-my-claudecode |
| [mksglu/hatice](https://github.com/mksglu/hatice) | TS, MIT | no releases |

Ideas worth keeping from them: oh-my-symphony's `verify:` command that decides when a lane is done
(scripts, not the agent); sortie's per-session agent notes, for example Claude needs `--verbose`
with `stream-json`.

A [port of Symphony to Claude Code](https://dev.to/mscalessio/i-ported-openais-symphony-to-claude-code-a-complete-build-diary-4h61)
hit three bugs its 144 unit tests missed, including `--mcp-config` swallowing the positional
prompt. Lesson: test against the real CLIs, not mocks.

## Agent CLIs, non-interactive mode

From `--help` on the author's machine; not run yet.

| Agent | Version | One-shot | No prompts |
| --- | --- | --- | --- |
| Claude Code | 2.1.322 | `claude -p <prompt>` | `--permission-mode auto` (also `acceptEdits`, `bypassPermissions`) |
| Codex | 0.167.1 | `codex exec <prompt>`, or stdin | `--sandbox workspace-write`; `--add-dir <dir>` for extra writable dirs |
| opencode | 2.2.6 | `opencode run <message>` | `--auto` |
| Kimi | 2.5.2 | `kimi -p <prompt>` | `--auto` (never ask), `-y` (ask when needed) |
| pi | 1.4.0 | `pi -p <prompt>` | no approval flag; `--tools` limits tools |
| Gemini | - | not installed | - |

## Backlog.md

Version 1.55.3. `backlog task list --json` returns `schemaVersion: 1` with `id`, `status`,
`labels`, `project`, `assignees`, `createdAt`, `updatedAt`. Filters: `-s`, `--project`, `-l`,
`--ready`; `--watch` emits changed lists (with `--json`). `task edit` takes `-s`, `--comment`,
`--plan`, `--add-label`. `BACKLOG_CWD` selects the backlog folder. The config has the statuses
To Do, Spec Review, In Progress, Review, Rework, Blocked, Done.
