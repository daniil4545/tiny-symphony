# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.0] - 2026-11-12

### Added

- `tiny-symphony run` and `run --once`: poll the tracker, run up to `max_agents` agents at once, each
  task in its own git worktree.
- A run stops when a human moves the card; a lock file keeps a second daemon out.
- Result checks before anything is written back: a plan for To Do, and for Review a clean tree, a
  pushed branch, a PR or MR and the project's `verify` command.
- Retries after 1, 5 and 15 minutes with the reason in the next prompt; Blocked after `max_attempts`.
  The attempt count is kept in the worktree, so a restart or `run --once` from cron does not reset it.
- The worktree is found by task id, so renaming a task does not lose it. `backlog` calls time out;
  a tracker error is not counted as an agent attempt.
- Leftover agents of a crashed daemon are killed on start; worktrees of Done tasks and old run logs
  are cleaned up.
- `tiny-symphony check`: config, tracker, workspace, agent commands, repos and prompt files, one line
  each, exit 1 on any failure.
- Agents verified by a smoke run: Claude Code, Codex, Kimi. Not verified: opencode, pi.
