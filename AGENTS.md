# tiny-symphony - context for AI agents

## Project

A small daemon: watches Backlog.md, runs a coding agent per task in a git worktree, applies the
agent's result back to the tracker. The behavior canon is `docs/spec.md`; read its Philosophy
before adding anything. Tasks are in the central backlog, project `tiny-symphony`.

## Principles

- One job. A feature that is not in the spec is a question to the user, not a change.
- Stdlib first. A new dependency needs a line in `docs/spec.md` Decisions.
- Test against the real agent CLIs before calling an agent supported.

## Stack

Go, one `tiny-symphony` binary. Not started yet.

## Workflow

- Tracker: Backlog.md, project `tiny-symphony`.
- Branches: GitHub flow, `main` plus `T-<N>-<name>` per task. No remote yet.
