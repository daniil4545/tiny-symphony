# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.2.0] - 2026-11-12

### Changed

- The tracker is tix. The agent takes the task, writes notes, opens the PR, puts the link into
  `external` and hands the task over itself; the daemon reads where the task ended up. Success no
  longer depends on a result file or the exit code.
- A new config: `watch`, `handoff_status`, `blocked_status`, `reopen_status`, `[repos]`. An optional
  `.tiny-symphony.toml` per project adds to the prompt and may lower `max_attempts` and
  `run_timeout_minutes`; it cannot name agent commands.
- Every daemon write (a note, a move to blocked or back) follows a fresh read and is skipped if
  a human changed the task meanwhile.
- `check` validates the watched statuses, the repositories and `gh`, and prints the config per project.

### Added

- A task left taken by a crashed run or a killed daemon returns to `open`; a run stops when a
  human removes the tag or takes the task back.
- PR watch: a note when the PR is merged, a note and a reopen when a check fails, at most
  `max_reopens` times. Off by default.
- Cleanup of worktrees and branches of closed tasks, switchable.
- The task is handed over under the actor `tiny-symphony:<agent>:<T-N>`, so its work is told apart
  from yours in the tix history.

### Removed

- Backlog.md support, `result.json`, the status pipeline and the per-status prompts.

### Known

- A task taken and returned between two ticks does not stop the agent; at worst it runs to the timeout.
- Live runs on 0.2.0: claude passed (took the task, opened a PR, handed it over). codex did the work;
  the PR step needs codex's own approval settings, outside tiny-symphony. kimi was not run: its login had expired.

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
