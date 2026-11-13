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

