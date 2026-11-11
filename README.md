# tiny-symphony

A tiny daemon that takes tasks from your tracker and hands them to a coding agent, each in its own
git worktree. Any agent that has a one-shot CLI mode works: Claude Code, Codex, opencode, Kimi, pi,
and the next one is a few lines of config.

Inspired by [OpenAI Symphony](https://github.com/openai/symphony), cut down to one job.

Status: S1 of v0.0.1. `tiny-symphony run --once` takes the ready tasks that carry an `agent:<name>`
label, runs the agent on each in a git worktree and writes the result back to Backlog.md. Not there
yet: checks of a result beyond its transition (a plan for To Do, finished work for Review), the loop (`run` without `--once`), `max_agents`, retries and Blocked on repeated failure,
`check`, cleanup of Done tasks. Tasks run one after another.

## Quick start

Needs `git`, [Backlog.md](https://github.com/MrLesk/Backlog.md) (`backlog`) 1.55 and an agent CLI.

1. `go build -o tiny-symphony .`
2. Copy `examples/config.toml` to `~/.config/tiny-symphony/config.toml` and `examples/prompts/` next
   to it. Set `backlog_dir`, `workspace_root`, a `[projects.<name>]` repo and the agent command.
3. The backlog needs the statuses To Do, Spec Review, In Progress, Review, Rework, Blocked, Done
   and a `projects` list that has the project name.
4. Give a task the project and the label `agent:claude`, then run `./tiny-symphony run --once`.

A To Do task goes to Spec Review with a plan, or to Blocked with a question. Prompts are
`prompts/to-do.md`, `in-progress.md`, `rework.md` (Go templates, an unknown variable is an error).
The agent writes `.tiny-symphony/result.json` in its worktree; see `docs/spec.md`, Agent contract.
`make smoke` (paid, needs `PAID_OK=1 MAX_USD=1`) runs the real `claude` on a toy task.

## Docs

| File | What |
| --- | --- |
| `docs/spec.md` | philosophy, pipeline, agent contract, config, decisions |
| `docs/flow.drawio` | the original pipeline drawing |
| `docs/research/T-660-prior-art.md` | Symphony, its ports, agent CLI flags |

## License

MIT
