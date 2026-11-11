# tiny-symphony

A tiny daemon that takes tasks from your tracker and hands them to a coding agent, each in its own
git worktree. Any agent that has a one-shot CLI mode can work: Claude Code, Codex and Kimi are verified, opencode
and pi are not yet (see Agents), and the next one is a few lines of config.

Inspired by [OpenAI Symphony](https://github.com/openai/symphony), cut down to one job.

Status: v0.0.1 (S4). `tiny-symphony run` polls every `poll_seconds`, runs up to `max_agents` agents at
once, takes ready tasks that carry an `agent:<name>` label, runs the agent on each in a git worktree
and writes the result back to Backlog.md. It checks the result before applying it: a plan for To Do,
and for Review a clean tree, a pushed branch, a PR or MR for the branch in any state (`gh` or `glab`) and the project's
`verify` command. A failed run is retried after 1, 5 and 15 minutes with the reason in the next
prompt; after `max_attempts` the task goes to Blocked with a comment. A card moved by a human stops
its run; `run_timeout_minutes` kills a hung one; a lock file under `workspace_root` keeps a second
daemon out; leftover agents of a crashed daemon are killed on start. Worktrees of Done tasks are
removed (the branch only if it is an ancestor of the repo HEAD; a squash-merged branch stays) and run logs older than `log_days` are deleted. `run --once`
does one tick and waits for its runs. `check` tests the config before the first run. Not there yet:
a live run under launchd, and the opencode and pi agents are not verified. Config is read at start;
restart to change it.

## Quick start

Needs `git`, [Backlog.md](https://github.com/MrLesk/Backlog.md) (`backlog`) 1.55 and an agent CLI.

1. `go build -o tiny-symphony .`
2. Copy `examples/config.toml` to `~/.config/tiny-symphony/config.toml` and `examples/prompts/` next
   to it. Set `backlog_dir`, `workspace_root`, a `[projects.<name>]` repo and the agent command.
3. The backlog needs the statuses To Do, Spec Review, In Progress, Review, Rework, Blocked, Done
   and a `projects` list that has the project name.
4. Run `./tiny-symphony check`: one line per check (tracker answers, workspace, agents on PATH with
   `{prompt}`, repos, forge CLI, prompt files); exit 0 means all ok. It starts no agent and writes nothing.
5. Give a task the project and the label `agent:claude`, then run `./tiny-symphony run` (or `run --once`).

A To Do task goes to Spec Review with a plan, or to Blocked with a question. Prompts are
`prompts/to-do.md`, `in-progress.md`, `rework.md` (Go templates, an unknown variable is an error).
The agent writes `.tiny-symphony/result.json` in its worktree; see `docs/spec.md`, Agent contract.
`make smoke AGENT=<name>` (paid, needs `PAID_OK=1 MAX_USD=1`) runs that real CLI on a toy task.

## Agents

An agent counts as supported only after a passing smoke run.

| Agent | CLI version | Command | Smoke run |
| --- | --- | --- | --- |
| claude | 2.1.322 | `claude -p --permission-mode auto {prompt}` | passed 2026-11-11 (S1) |
| codex | 0.167.1 | `codex exec --sandbox workspace-write {prompt}` | passed 2026-11-11, 48 s |
| kimi | 2.5.2 | `kimi -p {prompt}` | passed 2026-11-11, 61 s |
| opencode | 2.2.6 | `opencode run --auto {prompt}` | not verified: API key expired |
| pi | 1.4.0 | `pi -p {prompt}` | not verified: no API key |

## Docs

| File | What |
| --- | --- |
| `docs/spec.md` | philosophy, pipeline, agent contract, config, decisions |
| `docs/flow.drawio` | the original pipeline drawing |
| `docs/research/T-660-prior-art.md` | Symphony, its ports, agent CLI flags |

## License

MIT
