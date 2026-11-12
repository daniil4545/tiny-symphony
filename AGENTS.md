# tiny-symphony - context for AI agents

## Project

A small daemon: watches a Backlog.md board, runs a coding agent CLI per task in a git worktree,
checks the agent's result and applies it back to the board. Single user, own machine. The behavior
canon is `docs/spec.md`; read its Philosophy before adding anything. Structure: `docs/architecture.md`.
Tasks are in the central backlog, project `tiny-symphony`.

## Principles

- One job. A feature that is not in the spec is a question to the user, not a change.
- Stdlib first. A new dependency needs a line in `docs/spec.md` Decisions.
- The tracker is the state; in memory only runs and attempt counts. No database.
- The daemon is the only tracker writer; the agent returns `.tiny-symphony/result.json`.
- Test against the real agent CLIs before calling an agent supported (`make smoke`, paid, after approval).

## Stack

Go (version in `go.mod`), one `package main`, one dependency (`github.com/BurntSushi/toml`). Runs on
macOS and Linux; needs `git`, `backlog` 1.55 and `gh` or `glab`. CI: GitHub Actions.

## Structure

| Path | What |
| --- | --- |
| `*.go` | the daemon, one package; each `*_test.go` sits next to its file |
| `examples/` | sample config and prompts, also used by tests |
| `docs/` | `spec.md`, `architecture.md`, `research/` |
| `scripts/`, `Makefile`, `.golangci.yml`, `.github/` | gates; do not edit in a task |

## Commands

| What | Command |
| --- | --- |
| build | `make build` |
| fast tests (no network) | `make test-fast` |
| all tests (need `git`, `backlog`) | `make test` |
| format, vet, lint | `make fmt`, `make vet`, `make lint` |
| before a push | `make push-check` |
| before a PR into dev | `make pr-check` |
| run | `go run . run --once`, `go run . check` |
| paid smoke on a real agent | `PAID_OK=1 MAX_USD=1 make smoke AGENT=codex` |

## Process

- Branches: git-flow. `main` is releases, `dev` is integration, a task is `T-<N>-<name>` from `dev`.
  PR into `dev`; the human merges.
- Host: GitHub. CI runs `pr-check` on PRs into `dev`, `ci-check` on PRs into `main` and pushes.
- A user-visible change updates README, spec or CHANGELOG in its own commit.

## Critical invariants

Boundaries R1-R7 of `docs/spec.md`, in short:

- The daemon never touches a task without an `agent:<name>` label.
- It starts agents only inside `workspace_root`, never through a shell.
- It applies only checked results and only transitions of the pipeline table.
- It never merges, pushes or deletes an unmerged branch; one daemon per `workspace_root`.
