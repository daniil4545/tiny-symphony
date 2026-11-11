# tiny-symphony

A tiny daemon that takes tasks from your tracker and hands them to a coding agent, each in its own
git worktree. Any agent that has a one-shot CLI mode works: Claude Code, Codex, opencode, Kimi, pi,
and the next one is a few lines of config.

Inspired by [OpenAI Symphony](https://github.com/openai/symphony), cut down to one job.

Status: design. Nothing to run yet.

## Docs

| File | What |
| --- | --- |
| `docs/spec.md` | philosophy, pipeline, agent contract, config, decisions |
| `docs/flow.drawio` | the original pipeline drawing |
| `docs/research/T-660-prior-art.md` | Symphony, its ports, agent CLI flags |

## License

MIT
