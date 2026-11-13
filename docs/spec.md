# tiny-symphony: spec

Status: 0.1.0, accepted 2026-11-13. Inspired by [OpenAI Symphony](https://github.com/openai/symphony)
([SPEC.md](https://github.com/openai/symphony/blob/main/SPEC.md), read at `be10a1b`).
One task across the tracker, the daemon, git and GitHub: `docs/process.md`. Research:
`docs/research/T-704-github-pr-flow.md`, `docs/research/T-660-prior-art.md`.

tiny-symphony is a small daemon that finds tagged tasks in the watched statuses of a tracker and
runs a coding agent CLI on each in its own git worktree. The tracker owns the statuses and the
gates, the agent works with the tracker itself, the daemon says when something went wrong.

## Expected result

- `tiny-symphony run` against tix runs `agent:<name>` tasks from the statuses in `watch`.
- The agent takes the task, writes its notes, opens the PR, puts the link into `external` and hands
  the task over to `handoff_status`; on success the daemon writes nothing.
- The daemon writes only a note, a move to `blocked_status` or a move back to `reopen_status`.
- A central config and an optional project config, like a user and a project `CLAUDE.md`.
- PR watch and cleanup are modules with an on and off switch.

## Philosophy

1. **One job.** Find tagged tasks in the watched statuses, run an agent on each in its own worktree,
   report failures. Not a tracker, not a pipeline, not a merge bot.
2. **Each system owns its state.** Task and status: tracker. Branch: git. PR, CI, merge: GitHub.
   The daemon keeps nothing durable except the attempts and watch files in the worktree. No database.
3. **The agent works like a person.** Same CLI, same rules files, same tracker commands as an
   interactive session. The daemon adds the worktree, the actor name and a short prompt. An agent
   is an argv template in the config; a new one is a few lines, no code.
4. **Humans gate in the tracker.** A gate is a status the daemon does not watch.
5. **Modules are independent and can be turned off.** Dispatch is the core; the rest are switches.
6. **Small and boring.** One binary, stdlib first, no HTTP. A feature is added when its absence
   hurts in real use.

## Tracker interface

One Go interface, one adapter per tracker. 0.1.0 ships tix only. The adapter runs `tix` with the
working directory `workspace_root`, which is outside any git repository and any project path, so
`tix list` is not narrowed to the project of the current folder; the store comes from `TIX_HOME`
or the tix default. Every call has a timeout.

| Method | tix command | Used by |
| --- | --- | --- |
| `List(statuses)` | `tix list --status <s> --json`, once per status | dispatch, watch, cleanup |
| `Get(id)` | `tix show <id> --json` | before every write, after a run, reconcile |
| `Repos()` | `tix projects` (text columns: name, path (`-` is none), count, a note such as `not in catalog`) | dispatch: the repository of a project |
| `Note(id, text)` | `tix note <id> --as <actor> -- <text>` | every daemon write starts with a note |
| `Block(id, reason)` | `tix move <id> <blocked_status> -m <reason> --as <actor>` | after `max_attempts` |
| `Reopen(id)` | `tix move <id> <reopen_status> --as <actor>` | a run left the task taken; a PR did not land |

`tix move` takes `-m` only into blocked and closed, so the reason of `Reopen` goes into the note
before it. Moving out of in_progress drops the claim, so no separate release is needed.

Task fields the daemon reads: `id`, `title`, `project`, `status`, `tags`, `claim`, `external`,
`rank`, `created`, and the `After: T-N` lines of the body. Statuses are compared trimmed and
lowercased. With tix 0.9 statuses are names with a kind; the daemon takes names from its
config and needs no kind.

### Adapter contract

What any tracker must give the daemon. Each line is a capability, not a command, so tix 0.9
changes only the adapter and the config. Checked on 2026-11-12 against Linear, GitHub
Issues and Jira (`docs/research/T-704-github-pr-flow.md`, Tracker adapters).

| Capability | tix 0.8 | Others |
| --- | --- | --- |
| list tasks by status and tag | `list --status --json` | all three filter by status and label |
| one task with body and dependencies | `show --json`, `After:` | GitHub `blocked_by`; Linear, Jira relations |
| holder: who works on the task now | `claim` (expires after 3 h) | an assignee (GitHub, Jira) or a delegate (Linear); none expires |
| a note by a named actor | `note --as`, up to 300 characters | a comment by a bot or app user |
| a status move with a reason | `move -m` | a move plus a note; GitHub Issues has only open and closed, so a status there is a label or a Projects field |
| a link field | `external` | Linear attachments, Jira remote links, GitHub linked PRs |
| project to repository | `projects` | GitHub Issues: the repository that holds them; Linear, Jira: none, the central config maps it (`[repos]`) |

So the daemon knows statuses only as names from its config (the adapter maps a name to a status, a
label or a field), the holder only as "an actor or nobody" (an expiry is the adapter's), parents
only as "not dispatched". The agent writes the tracker itself, so it needs that tracker's own tool
(`tix`; for others `gh`, an MCP server: not checked). An adapter that lacks a capability
turns the module that needs it off and says so in `check`.

## Actor

Each run has the actor `tiny-symphony:<agent>:<T-N>`. tix takes `--as` before the agent's own
session variables (`CLAUDE_CODE_SESSION_ID`, `CODEX_THREAD_ID`), so the prompt gives the agent this
name for every `tix` command. A claim with this name is ours; any other live claim belongs to
someone else. The daemon writes with `--as tiny-symphony`, so its own writes are told apart from
the agent's in the tix history.

## Run

Every `poll_seconds` the daemon reconciles, collects and dispatches. One goroutine owns the
scheduler state (running runs, retry times); workers report their outcome on a channel. A bad
config stops the process at start (it is read once); a tracker read error skips the tick and
leaves the runs alone; a tracker error inside a run is not an attempt of the agent. Logs are
`key=value` lines (`log/slog`) with the task id and the outcome.

1. **Collect.** Tasks in the `watch` statuses with a tag `agent:<name>` of a known agent, no live
   claim, not an epic (tag `epic`; with tix 0.9 a task with children), every `After:` task closed
   (`list --json` has no body, so `After:` is read with `Get`, only for candidates), a project with
   a repository. Order: rank, then oldest, then id.
2. **Dispatch** while fewer than `max_agents` runs go and the task is not running or in backoff.
3. **Worktree** on `T-<N>-<slug>` from the project repo, found by id (not by title, so a renamed
   task keeps it) and reused across runs. `.tiny-symphony/` is kept out of git (`info/exclude`).
4. **Agent** in the worktree, own process group, log in `.tiny-symphony/`, pid file, prompt from the
   template (Prompt).
5. **Reconcile** every tick: a run is stopped (SIGTERM to the group, SIGKILL after 10 s), with no
   attempt counted but with the next backoff step, when its task lost its `agent:` tag, or when our
   actor had taken the task and it is now back in a watched status or held by another actor: a
   human or a session took it over. Other status moves do not stop it: the agent moves the task
   itself when it hands over. A run past `run_timeout_minutes` is stopped the same way and counts
   as a failed attempt.
6. **Outcome.** After exit the daemon reads the task again. The tracker state decides, not the
   exit code:

| Task after the run | Outcome | Daemon writes |
| --- | --- | --- |
| left the watched statuses and in_progress (handed over, blocked, closed), whoever moved it | success, attempts reset | nothing |
| in a watched status | failed attempt | nothing |
| in_progress with our claim | failed attempt | note with the reason, `Reopen` |
| in_progress with no live claim (empty or expired) | failed attempt | note, `Reopen` |
| in_progress with another live claim | failed attempt | one note "left in_progress by <claim>; the agent may have missed `--as`"; no move |
| tag removed | run was stopped, no attempt | nothing |

**Stuck in_progress.** A task found in `in_progress` between runs (the daemon was killed, or the
write at the end of a run failed) with an `agent:` tag, no run and an empty or our own actor's
claim, live or expired, gets a note and `Reopen` on the next tick. An empty claim counts as a
failed attempt; our own claim only as the next backoff step (the daemon died or stopped, not the
agent). Another holder's task is left alone.

Daemon notes stay under 300 characters: the reason in a few words and the log path relative to
`workspace_root` (`T-12-slug/.tiny-symphony/20261112T091500Z.log`, UTC start time). Failed attempts retry after 1, 5 and 15
minutes; the reason (exit code, timeout, "task left taken") goes into the next prompt. After
`max_attempts` the daemon writes a note with the reason and the log path, calls `Block`, and resets
the count, so a human who moves the task back gets a fresh limit. Attempts and reopens live in
`.tiny-symphony/attempts` in the worktree, so a restart or `run --once` from cron does not give a
failing task more runs. The agent works in that worktree and can edit the file: a damaged file
counts as the limit reached, a deleted one resets the count (known residual risk).

**Every daemon write is guarded.** Right before `Note`, `Block` or `Reopen` the daemon calls
`Get` and writes only if the task has not changed since the read that decided the write: the same
status, claim and `external`. Anything else means someone moved it, and the daemon writes nothing.
`tix move` has no compare-and-set, so a move in the millisecond between `Get` and the write is a
known residual race.

On start the daemon takes the lock of `workspace_root` and kills leftover agents found by their pid
files (S6).

### Supervision

The daemon does not trust the agent's word; it checks the tracker.

| ID | Failure | How the daemon sees it | What it does |
| --- | --- | --- | --- |
| S1 | agent crashed | non-zero exit or killed by a signal, task not handed over | failed attempt |
| S2 | agent hung | `run_timeout_minutes` passed | kills the process group, failed attempt |
| S3 | task taken over | tag removed, or the taken task is back in a watched status or held by another actor | kills the process group, no attempt counted |
| S4 | no handover, or a bad project config | exit 0 with the task not moved; a project file that does not parse or sets a forbidden key | failed attempt |
| S5 | agent keeps failing | `max_attempts` failed attempts on one task | note with the reason and log path, `Block`, count reset |
| S6 | daemon restarted | a pid file in a worktree with that process still alive | kills the leftover process group, then dispatches as usual |

**Known deviation.** Reconcile sees our claim only on a tick. An agent that takes a task and
gives it back between two ticks is not stopped; in the worst case it runs on until the timeout.

## Prompt

One built-in template for every status, strict (`text/template`, `missingkey=error`). Variables:
`id`, `title`, `branch`, `worktree`, `actor`, `handoff_status`, `attempt`, `last_error`. The text
says: run every `tix` command with `--as {{.actor}}`; take the task; read it with `tix show`; work
only in this worktree, not in the project path that `tix take` prints; put the PR link into
`external`; hand over with `tix move {{.id}} {{.handoff_status}} -m "Ждёт: review PR"` (a move into
blocked needs a reason). It also says what not to do, because the tracker rules of an interactive
session say otherwise: no `tix done`; no `tix take --release` when the work fails, just a note and
exit, the daemon returns the task; no `--steal` when `take` is refused, just exit; new tasks get no
`agent:` tag, only a human adds it. Project specifics come from the agent's own rules files and
`prompt_append`. The task text is not copied into the prompt: the agent reads the latest version
and the human's comments from the tracker.

## Config

Two layers.

1. Central: `~/.config/tiny-symphony/config.toml`. Everything below.
2. Project, optional: `.tiny-symphony.toml` in the main checkout of the project (the path from
   `tix projects`), read from disk at each dispatch. It may set only `prompt_append` (added after
   the central one), `max_attempts` (the attempt limit for the tasks of this project) and
   `run_timeout_minutes`, the last two no higher than the central ones (a PR must not lift the
   spending limit). Any other key, or a file that does not parse, is a failed attempt with the
   reason in the note at `Block`, so backoff and `max_attempts` bound it like any other failure.
   Agent commands never come from a repository: an agent could change them in a PR and the daemon
   runs outside any sandbox (R7).

```toml
tracker = "tix"
workspace_root = "~/.local/share/tiny-symphony/work"   # must not be inside a git repository
watch = ["open"]              # statuses where agents pick work up
handoff_status = "blocked"    # where the agent puts a finished task (tix 0.9: a review status)
blocked_status = "blocked"    # where the daemon moves a task after max_attempts
reopen_status = "open"        # where the daemon returns a task; must be in watch
poll_seconds = 30
max_agents = 2
run_timeout_minutes = 60
max_attempts = 3
prompt_append = ""

[repos]                       # project to repository, for projects the tracker has no path for
# toy = "~/Dev/toy-repo"

[agents.claude]
cmd = ["claude", "-p", "--permission-mode", "auto", "{prompt}"]
[agents.codex]                # workspace-write keeps .git read-only and the network off: no commit, no push
cmd = ["codex", "exec", "--sandbox", "danger-full-access", "{prompt}"]
[agents.kimi]
cmd = ["kimi", "-p", "{prompt}"]

[watch_pr]
enabled = false
poll_minutes = 5
max_reopens = 2               # PRs that did not land, per task, before the daemon stops reopening

[cleanup]
enabled = true
log_days = 14
```

`workspace_root` is required; the other settings default to the values above. `~` is expanded in
path fields and in the first element of an agent command; other argv elements are passed as
written. `{prompt}` is replaced by the rendered prompt as one argv element; no shell is involved.
At least one agent is needed to do any work. A task whose project has no repository is skipped
with a log line.

Live runs: claude passed on a clone of ci-sandbox; codex needs its own approval settings
for the PR step and kimi was not run (login expired); opencode and pi never were. An agent counts as supported after `make smoke AGENT=<name>` (paid, `PAID_OK=1 MAX_USD=1`).

## Commands

| Command | What it does |
| --- | --- |
| `tiny-symphony run` | the loop, in the foreground; a supervisor such as launchd or systemd can restart it (not tried) |
| `tiny-symphony run --once` | one tick, then waits for its runs and exits; exit 1 if a run failed |
| `tiny-symphony check` | config, tracker, agents on PATH, repos, `gh` when the watch is on; prints the merged config per project; one line per check, exit 1 on any FAIL; starts no agent, writes and creates nothing |

Both `run` and `check` take `--config <path>` (default `~/.config/tiny-symphony/config.toml`).

`check` fails, and `run` refuses to start, when: `watch` is empty or names a status tix refuses;
`watch` contains `handoff_status`, `blocked_status` or `in_progress` (every handover or take would
look like a failure, a loop that costs a run each time); `reopen_status` is not in `watch`;
`workspace_root` is inside a git repository or a project path; a project path has a space.

## Modules

| Module | What | Switch |
| --- | --- | --- |
| dispatch | Collect, dispatch, reconcile, outcome, retries, lock | always on |
| PR watch | every `poll_minutes`, for each closed task with a worktree, no running run and a PR link in `external`: `gh pr view --json state,mergedAt,mergeCommit,mergeStateStatus,statusCheckRollup`. Merged: note "PR merged <sha of mergeCommit>", the task is done for the watch. A failed check (`FAILURE`, `ERROR`, `TIMED_OUT`, `CANCELLED`, `STARTUP_FAILURE`, `ACTION_REQUIRED`; pending, neutral and skipped are not failures) or `mergeStateStatus` `DIRTY`: note "PR needs work: <reason>. Back to <reopen_status> (reopen N of M)", `Reopen`, reopens + 1. Closed unmerged: one note "PR closed without merge. The task stays closed, the branch stays." At `max_reopens`: one note "PR does not land (<reason>), reopened N times. Needs a human.", no more reopens. Each of these marks (merged, closed, limit) is kept in `.tiny-symphony/watch`, so a restart writes no note twice. A `gh` error: log line, next round | `[watch_pr] enabled` |
| cleanup | for each closed task with no running run: remove its worktree (`git worktree remove`, which refuses a dirty tree), `git branch -d` (refuses an unmerged branch, so a squashed branch stays). Logs older than `log_days` are removed on every tick, with this module on or off. With the watch on it waits until the watch has seen the PR merged or closed, or the task has no PR link | `[cleanup] enabled` |
| check | see Commands | command |

The watch never switches auto-merge on and never merges: a human merges (D7). A task leaves the
watch when its worktree is removed, so the list stays short.

## Boundaries

What the daemon never does, whatever the input. Each line gets a test.

- R1: Starts an agent outside `workspace_root`, or with a cwd other than the task's worktree.
- R2: Touches a task without an `agent:<name>` tag.
- R3: Puts a shell between itself and the agent.
- R4: Runs a second instance against the same `workspace_root`.
- R5: Merges, pushes, or deletes an unmerged branch.
- R6: Writes the tracker other than `Note`, `Block` and `Reopen`, or without a guarding `Get`.
- R7: Takes an agent command from anywhere but the central config.

## Decisions

- D1 Go, one binary. Rejected: Rust. The daemon waits on agents that run for minutes, so speed and
  memory do not matter; Go has `os/exec`, signals and templates in stdlib.
- D2 TOML config, the only dependency (`github.com/BurntSushi/toml`, MIT). Rejected: JSON (no
  comments), YAML (bigger dependency).
- D3 The agent writes the tracker itself, the daemon is not its only writer. Reason: the
  owner, 12.11: an agent working with the tracker itself is close to the main feature of tix. Cost: the
  agent needs write access to the tracker store, git and the network, so codex runs with
  `danger-full-access` and the worktree is a convention, not a sandbox (unchecked: live run); the
  daemon checks only where the task ended up.
- D4 The tracker is an interface; tix is the first and only adapter in 0.1.0.
- D5 Success is read from the tracker after the run, not from the exit code.
- D6 The project config cannot name commands (R7).
- D7 A human merges; the watch only reports. Auto-merge by the agent or the daemon was rejected
  for now (`docs/process.md`, Merge). Owner, 12.11.
- D8 `handoff_status` is `blocked` until tix 0.9 brings a review status; the same release may
  bring a merging status for the watch. Owner, 12.11.
- D9 An agent that closes the task itself is a success, though `tix show --json` does not say who
  closed it. Rejected: reading the store's git log and moving the task back. Owner, 12.11.

## Acceptance

Tests use the real `tix` binary on a temporary store (`TIX_HOME`), a fake agent script and a fake
`gh` on PATH. "The daemon writes nothing" is checked in the store's git log by the actor.

1. Happy path: a task with `agent:claude` in `open`; the fake agent takes it with the given actor,
   sets `external` and moves it to `handoff_status`. The run is a success, no commit by
   `tiny-symphony`.
2. The fake agent exits 0 without moving the task: failed attempt, retry after backoff; after
   `max_attempts` a note and blocked by `tiny-symphony`, and the count is reset.
3. The fake agent takes the task and crashes: note, task back to `reopen_status`, retried.
4. The fake agent hands over and exits 1: success.
5. A human (another actor) moves the task between the run's end and the daemon's write: the daemon
   writes nothing.
6. The tag is removed during a run, or a human moves the taken task back to `open`, or another
   actor steals it: the agent is stopped, no attempt, next backoff step. A fake agent that releases
   the task itself and sleeps 60 s is stopped the same way and is not dispatched again at once.
   6a. The fake agent runs without `--as` and crashes: the task is in_progress with a foreign
   claim; one note, no move, no second note on the next tick.
7. A project `.tiny-symphony.toml` adds to the prompt; one with `[agents.x]` fails the dispatch.
8. Watch on, fake `gh`: merged gives a note and the worktree removed; a failed check gives a note
   and `Reopen`, until `max_reopens`, then exactly one note over several rounds; pending gives
   nothing; `gh` failing gives a log line and no tracker write. Watch off: `gh` is never called.
9. `tix` missing or failing: the tick is skipped, runs go on.
10. `check` fails, and `run` exits with an error, on each case of Commands; a project file with a
    higher `max_attempts` fails the dispatch.
11. One live run per supported agent (claude, codex, kimi) on a throwaway repo: the claim in the
    store carries `tiny-symphony:<agent>:<T-N>`; the owner reads the log. Before the release.
12. `make ci-check` green; R1-R7 each have a test.

## Left out

Web UI and HTTP API; database; hooks; per-state concurrency; multi-turn sessions inside one run;
stall detection by output (one-shot agents stay silent until they finish; the timeout covers
hangs); token and cost accounting; auto-merge; trackers other than tix; live config reload
(restart the daemon); a per-project `setup` command. Single user, own machine: a task text is a
prompt, so anyone who can write tasks can run code; do not point the daemon at a tracker others
write to.

## Open questions

- Q1 Tasks without code (research, writing): a plain folder instead of a worktree, or not yet.
- Q2 Subscription limits: an agent that hits its limit fails on every task. A pause for that agent
  after N failures in a row across tasks would save attempts; the daemon cannot tell a limit from
  other failures without parsing output.
