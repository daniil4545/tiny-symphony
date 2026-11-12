# tiny-symphony - context for AI agents

## Project

A small daemon that runs a coding agent CLI per tracker task in a git worktree. Single user, own
machine. The code is 0.2.0, a connector to tix: the agent writes the tracker itself, the daemon
only finds tagged tasks, runs agents and says when something went wrong. `docs/spec.md` is the
canon; the process across tracker, git and GitHub is `docs/process.md`. Read the Philosophy of the
spec before adding anything. Structure: `docs/architecture.md`. Tasks are in tix (`tix ctx`),
project `tiny-symphony`.

## Principles

- One job. A feature that is not in the spec is a question to the user, not a change.
- Stdlib first. A new dependency needs a line in `docs/spec.md` Decisions.
- The tracker is the state; in memory only runs and attempt counts. No database.
- The agent writes the tracker; the daemon only `Note`, `Block` and `Reopen`, each after a
  guarding read (R8).
- Test against the real agent CLIs before calling an agent supported (`make smoke`, paid, after approval).

## Stack

Go (version in `go.mod`), one `package main`, one dependency (`github.com/BurntSushi/toml`). Runs on
macOS and Linux; needs `git`, `tix` 0.8.1 and `gh`. CI: GitHub Actions.

## Structure

| Path | What |
| --- | --- |
| `*.go` | the daemon, one package; each `*_test.go` sits next to its file |
| `examples/` | sample config, also used by tests |
| `docs/` | `spec.md`, `architecture.md`, `process.md`, `research/` |
| `scripts/`, `Makefile`, `.golangci.yml`, `.github/` | gates; do not edit in a task |

## Commands

| What | Command |
| --- | --- |
| build | `make build` |
| fast tests (no network) | `make test-fast` |
| all tests (need `git`, `tix`) | `make test` |
| format, vet, lint | `make fmt`, `make vet`, `make lint` |
| before a push | `make push-check` |
| before a PR into dev | `make pr-check` |
| run | `go run . run --once`, `go run . check` |
| paid smoke on a real agent | `PAID_OK=1 MAX_USD=1 make smoke AGENT=codex` |

## Process

- Branches: git-flow. `main` is releases, `dev` is integration, a task is `T-<N>-<name>` from `dev`.
  PR into `dev`; after review and green CI the agent merges it through merge-gate. Only the human
  merges into `main`.
- Host: GitHub. CI runs `pr-check` on PRs into `dev`, `ci-check` on PRs into `main` and pushes.
- A user-visible change updates README, spec or CHANGELOG in its own commit.

## Critical invariants

Boundaries R1, R2, R5-R9 of `docs/spec.md`. In short:

- The daemon never touches a task without an `agent:<name>` label.
- It starts agents only inside `workspace_root`, never through a shell.
- Its tracker writes are only `Note`, `Block` and `Reopen`; agent commands come only from the central config.
- It never merges, pushes or deletes an unmerged branch; one daemon per `workspace_root`.
