# tiny-symphony

A small daemon that finds tasks tagged for an agent in [tix](https://github.com/daniil4545/tix) and
runs a coding agent CLI on each in its own git worktree. The agent works with the tracker itself:
takes the task, writes notes, opens the PR, hands the task over. The daemon says when something
went wrong. Cut down from [OpenAI Symphony](https://github.com/openai/symphony) to one job.

## Stage

v0.2.0, a prototype for one user on one machine. Works and covered by tests: the polling loop with
bounded concurrency, worktree per task, success read from the tracker after the run, retries with
backoff, a task blocked after `max_attempts`, return of a task left taken, stop when a human
removes the tag or takes the task back, a lock against a second daemon, a project config
(`.tiny-symphony.toml`), PR watch and cleanup as switchable modules, `check`. Only tix is
supported as a tracker. Not done: a run under launchd or systemd (the daemon runs in the
foreground), the config is read at start only. See `docs/spec.md`, Left out and Open questions.

## Quick start

Needs Go 1.26, `git`, [tix](https://github.com/daniil4545/tix) 0.8.1, `gh` (for the PR watch) and
an agent CLI from the table below.

1. Build: `git clone https://github.com/daniil4545/tiny-symphony && cd tiny-symphony && go install .`
   (the binary lands in `$(go env GOPATH)/bin`).
2. Config: copy `examples/config.toml` to `~/.config/tiny-symphony/config.toml`. Set
   `workspace_root` (outside any git repository) and an agent command. A project needs a path in
   `tix projects`, or a line in `[repos]`. Optional: `.tiny-symphony.toml` in the project checkout
   with `prompt_append` and lower `max_attempts` or `run_timeout_minutes`.
3. Check: `tiny-symphony check` prints one line per check and exits 1 on any FAIL. It starts no
   agent and writes nothing. It checks the config, that tix answers, `workspace_root`, agent
   commands on PATH, repositories, and `gh` when the watch is on.
4. Run: tag a task `agent:<name>` (for example `agent:claude`) and put it in `open`, then
   `tiny-symphony run --once` for one tick, or `tiny-symphony run` for the loop.

The agent takes the task, works in its worktree, puts the PR link into `external` and moves the
task to `handoff_status`. The human reads the PR, merges it and closes the task. The daemon merges
nothing. Process across tix, git and GitHub: `docs/process.md`.

Tests and linter: `make test` (needs `git` and `tix` on PATH) and `make lint`.

## Agents

The agent writes the tracker, so it needs write access to the tix store, git and the network. Codex
runs with `--sandbox danger-full-access`: with `workspace-write` it cannot commit or push.

| Agent | Command | Live run on 0.2.0 |
| --- | --- | --- |
| claude | `claude -p --permission-mode auto {prompt}` | passed 2026-11-12 on a clone of ci-sandbox: took the task, opened a PR, handed over |
| codex | `codex exec --sandbox danger-full-access {prompt}` | not verified: the run works, the PR step needs codex's own approval settings |
| kimi | `kimi -p {prompt}` | not checked: the CLI login had expired |

A live run is `make smoke AGENT=<name>` (paid, needs `PAID_OK=1 MAX_USD=1`); with
`LIVE_REPO=<GitHub URL>` the task runs in a clone of that repository and the PR the agent puts into `external` is closed after the run.
opencode and pi were never verified. Any other CLI with
a one-shot mode is a few lines in `[agents.<name>]`.

## Development

Checks: `make lint`

## Docs

| Question | Where |
| --- | --- |
| how it is built, where things are | `docs/architecture.md` |
| what it does, config, decisions | `docs/spec.md` |
| rules for agents working on this repo | `AGENTS.md` |
| what changed | `CHANGELOG.md`, `git log` |
| Symphony and agent CLI research | `docs/research/T-660-prior-art.md` |
| one task across tracker, daemon, git and GitHub | `docs/process.md` |
| GitHub merge, CI and bot practices | `docs/research/T-704-github-pr-flow.md` |

MIT, see `LICENSE`.
