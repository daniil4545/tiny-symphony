# tiny-symphony: process map

Draft for 0.2.0, 2026-11-12. One task from the board to `main`, across four systems: the tracker
(tix), the daemon, the git repository and GitHub. Who moves what, who writes where, what happens
when a step fails. Sources: `docs/research/T-704-github-pr-flow.md`.

## Principles

1. **Each system owns its state.** The tracker owns the task and its status, git owns the branch,
   GitHub owns the PR, CI and the merge. The daemon owns nothing durable: it reads and nudges.
2. **The agent works like a person.** It takes the task in the tracker, writes its plan and notes
   there, commits, pushes, opens the PR and puts its link into the task (`external`). The same rules
   as an interactive session, from the same `CLAUDE.md` or `AGENTS.md`.
3. **The human gates in the tracker.** Every human decision is a status move on the board. The
   human never has to open GitHub to move work on.
4. **The daemon writes only to say something went wrong.** Its only tracker writes are a note and a
   move to blocked (agent gave up) or back to open (PR did not land). Nothing else.
5. **Modules are separate and can be turned off.** Dispatch is the core; PR watch and cleanup are
   switches in the config. With all switches off the daemon is a task launcher.

## Systems

| System | Owns | Written by | Read by the daemon with |
| --- | --- | --- | --- |
| tix | task, status, claim, notes, `external` | agent, human, daemon (note, blocked, open) | `tix list --json`, `tix show --json` |
| daemon | running runs, retry times in memory; attempts file in the worktree | daemon | - |
| git repo | worktree `T-<N>-<slug>`, commits, branch | daemon (worktree add and remove), agent (commit, push) | `git worktree list`, `git branch -d` |
| GitHub | PR, CI, auto-merge, merge | agent (PR), GitHub (CI, merge), human (review, optional) | `gh pr view --json state,mergedAt,mergeStateStatus,statusCheckRollup,autoMergeRequest` |

## Main path

Statuses are names from the config; with tix 0.8 they are the five built-in ones. Watched statuses
(default `open`) are where an agent picks work up; every other status waits for a human.

| # | Who | System | What happens | Task status after |
| --- | --- | --- | --- | --- |
| 1 | human | tix | creates a task with criteria, adds the tag `agent:<name>`, puts it in a watched status | open |
| 2 | daemon | tix | sees the task in a watched status, with the tag, no live claim, not an epic | open |
| 3 | daemon | git | adds or reuses the worktree on branch `T-<N>-<slug>` | open |
| 4 | daemon | agent CLI | starts the agent in the worktree with a short prompt and `TIX_ACTOR=tiny-symphony:<agent>` | open |
| 5 | agent | tix | `tix take T-N`: holds the task | in_progress |
| 6 | agent | git, GitHub | works, commits, pushes, opens the PR, runs the project checks | in_progress |
| 7 | agent | tix | `tix set T-N external=<PR url>`, a note with the summary, then hands over: blocked with "Ждёт: review PR" (0.8) or a review status (0.9) | blocked or review |
| 8 | daemon | tix | after exit reads the task: moved out of the watched status and claim released, so the run succeeded | blocked or review |
| 9 | human | GitHub, tix | reads the PR (the link is in the task), then closes the task, or comments and moves it back to a watched status | closed or open |
| 10 | GitHub | GitHub | CI runs; the PR is merged (by the human, or by auto-merge, see below) | closed |
| 11 | daemon (watch) | GitHub, tix | for a closed task with a PR link: merged, so a note "merged <sha>" | closed |
| 12 | daemon (cleanup) | git | removes the worktree, deletes the branch if merged (`git branch -d`) | closed |

A comment from the human in step 9 sends the task back to step 2: the same worktree and branch,
the agent reads the comments in `tix show`.

## When a step fails

| Step | Failure | Who sees it | What happens |
| --- | --- | --- | --- |
| 4-7 | agent crashed or timed out | daemon: exit code, `run_timeout` | failed attempt; retry after 1, 5, 15 min with the reason in the prompt |
| 4-7 | agent exited 0 but left the task in a watched status or kept the claim | daemon: re-reads the task | failed attempt; the daemon releases the claim |
| 4-7 | the same task failed `max_attempts` times | daemon | note with the reason and log path, task to blocked; the human decides |
| 5 | another session holds the task | tix refuses `take` | the agent exits; no attempt counted, the task waits for the claim to end |
| 5-7 | human removed the tag, or someone else took the claim | daemon: next tick | the run is stopped, no attempt counted |
| 7 | no PR link in `external` | daemon (watch on) | not a failure of the run; the watch ignores the task, cleanup keeps the unmerged branch |
| 10 | CI red or merge conflict after close | daemon (watch): `statusCheckRollup`, `mergeStateStatus` = `DIRTY` | note with the reason, task back to open; the agent fixes it on the same branch |
| 10 | PR closed without merge | daemon (watch): `state` = `CLOSED`, no `mergedAt` | note "PR closed, not merged"; the task stays closed, the branch stays |
| any | tracker does not answer | daemon: CLI error or timeout | tick skipped, runs left alone |
| any | daemon crashed | daemon on start: pid files | leftover agents killed, attempts kept in the worktree |

## Merge: variants, not decided

GitHub facts that shape this (`docs/research/T-704-github-pr-flow.md`):

- Auto-merge is switched on per PR and waits only for the required checks and reviews of the base
  branch.
- `gh pr merge --auto` merges at once when the PR is already mergeable (state CLEAN or UNSTABLE).
  With no required check on `main` that means at once, even with checks pending or red.
- The author of a PR cannot approve it, and the agent pushes as the owner, so required reviews do
  not work for one person.
- tarn, tix, famulus and ci-sandbox protect `main` with the required check `gate` and allow
  auto-merge. Private repositories on the free plan cannot have protection rules.
- Symphony forbids auto-merge for the same reason: "no required checks so auto-merge can skip
  tests". Its agent waits for green checks and merges with `gh pr merge --squash` itself.

| Variant | Who merges | Human gate | Needs |
| --- | --- | --- | --- |
| A. Agent at PR time | GitHub, after the agent runs `gh pr merge --auto` | none: the PR lands on green CI, before the human looks | a required check on `main` |
| B. Daemon at the human's gate | GitHub, after the daemon runs `gh pr merge --auto --match-head-commit <sha>` when the human moves the task to a "merging" status; or the daemon itself after green checks where no required check exists | the status move | a required check, or the daemon reads `statusCheckRollup` before it merges |
| C. Human | the human in GitHub | the merge button | nothing |

Until this is decided the watch only reports: it never switches auto-merge on and never merges
(boundary R7). B is the closest to Symphony's "Merging" status. Linear does it the other way round:
the tracker follows PR events (opened, so In Progress; merged, so Done).

## Modules and switches

| Module | Config | Off means |
| --- | --- | --- |
| dispatch (core) | `tracker`, `watch`, `[agents.*]` | - |
| tracker adapter | `tracker = "tix"` | - (one adapter per tracker behind one interface) |
| PR watch | `[watch_pr] enabled = true` | closed tasks are not checked against GitHub |
| cleanup | `[cleanup] enabled = true`, `log_days` | worktrees and logs stay until removed by hand |
| check | `tiny-symphony check` | - |
