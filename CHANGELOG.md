# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [0.1.0] - 2026-11-13

First release.

### Added

- `tiny-symphony run` and `run --once`: poll [tix](https://github.com/daniil4545/tix) for tasks
  tagged for an agent, run up to `max_agents` agents at once, each task in its own git worktree.
- The agent works with the tracker itself: takes the task, writes notes, opens the PR, puts the link
  into `external` and hands the task over. The daemon reads where the task ended up; success does not
  depend on the exit code.
- Retries with backoff and the reason in the next prompt; the task is blocked after `max_attempts`.
- A task left taken by a crashed run or a killed daemon returns to `open`; a run stops when a human
  removes the tag or takes the task back. A lock file keeps a second daemon out.
- Every daemon write (a note, a move to blocked or back) follows a fresh read and is skipped if a
  human changed the task meanwhile. Its writes go under the actor `tiny-symphony:<agent>:<T-N>`.
- Config: `watch`, `handoff_status`, `blocked_status`, `reopen_status`, `[repos]`. An optional
  `.tiny-symphony.toml` per project adds to the prompt and may lower `max_attempts` and
  `run_timeout_minutes`; it cannot name agent commands.
- PR watch: a note when the PR is merged, a note and a reopen when a check fails, at most
  `max_reopens` times. Off by default.
- Cleanup of worktrees and branches of closed tasks, switchable.
- `tiny-symphony check`: config, tracker, statuses, workspace, agent commands, repositories and
  `gh`, one line each, exit 1 on any failure.

### Known

- A task taken and returned between two ticks does not stop the agent; at worst it runs to the timeout.
- Live runs: claude passed (took the task, opened a PR, handed it over). codex did the work; the PR
  step needs codex's own approval settings, outside tiny-symphony. kimi was not run: its login had
  expired.
