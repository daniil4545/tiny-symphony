# T-704 GitHub PR flow

Checked 2026-11-12: docs and READMEs read, the `gh` source read at `233b470`, nothing run against
real PRs. Question: which GitHub practices go into the process map of 0.2.0, so that the daemon
only polls the tracker while the merge and the task status follow.

## Verdict

There is nothing to take whole. The closest design is Symphony: the human moves the issue to
"Merging", the agent waits for green checks and merges itself. From GitHub take
`gh pr merge --squash --match-head-commit <sha>` and polling `gh pr view --json`. Native
auto-merge is safe only where `main` has a required check. Confidence medium.

## Facts

| Fact | Source |
| --- | --- |
| Auto-merge merges after all required reviews and required status checks pass | [docs](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/incorporating-changes-from-a-pull-request/automatically-merging-a-pull-request) |
| Without required status checks the branch can be merged at any time | [docs](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) |
| `gh pr merge --auto` merges at once when the PR is CLEAN, HAS_HOOKS or UNSTABLE; auto-merge is set only when it is BLOCKED, BEHIND and alike | [merge.go#L593](https://github.com/cli/cli/blob/233b470d3a189e89cf6fa69624e11c3fa9387d81/pkg/cmd/pr/merge/merge.go#L593) |
| PR authors cannot approve their own PRs; owners and admins can merge without an approving review | [docs](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/reviewing-changes-in-pull-requests/approving-a-pull-request-with-required-reviews) |
| Auto-merge is turned off by a push from a user without write access, or when the base changes | [docs](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/incorporating-changes-from-a-pull-request/automatically-merging-a-pull-request) |
| `gh pr view --json` gives `state`, `mergedAt`, `mergeable`, `mergeStateStatus`, `statusCheckRollup`, `reviewDecision`, `autoMergeRequest` | [gh manual](https://cli.github.com/manual/gh_pr_view) |
| A merge queue pays off with many PRs a day from many users | [docs](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) |
| The owner's repositories: auto-merge allowed and `main` protected by the required check `gate` on tarn, tix, famulus, ci-sandbox; no rulesets anywhere; private repositories on the free plan answer 403 for protection rules | `gh api repos/daniil4545/<repo>/branches/main/protection`, 2026-11-12 |

## Donors

**Symphony, statuses Human Review, Merging, Rework.** The agent hands over only with green checks.
The human approves in the tracker by moving the issue to Merging, or sends it to Rework. In Merging
the agent runs its `land` skill in a loop until the PR is merged, then sets Done. `land` merges a
conflict with `origin/main` and pushes, fixes a red check with a commit, and forbids auto-merge:
"Do not enable auto-merge; this repo has no required checks so auto-merge can skip tests."
([WORKFLOW.md](https://raw.githubusercontent.com/openai/symphony/main/elixir/WORKFLOW.md),
[land](https://raw.githubusercontent.com/openai/symphony/main/.codex/skills/land/SKILL.md)).
Difference: there Done comes after the merge. Take: a "merging" status as the human's gate.

**Linear GitHub automation.** The tracker follows PR events: drafted, opened, review requested,
ready for merge, merged; by default In Progress on open and Done on merge. Ready for merge does not
fire if any check, even a non-required one, failed. Rules can depend on the target branch
([docs](https://linear.app/docs/github)). Difference: Linear gets webhooks in the cloud; a local
tracker can only poll. Take: the same event-to-status idea, with polling.

**Renovate and Dependabot.** Renovate switches native auto-merge on when it creates the PR and
waits for the required tests; without tests it needs an explicit `ignoreTests`; a PR behind the
base is rebased on the next run ([Renovate](https://docs.renovatebot.com/key-concepts/automerge/)).
Dependabot does the same from a workflow with `gh pr merge --auto`
([docs](https://docs.github.com/en/code-security/dependabot/working-with-dependabot/automating-dependabot-with-github-actions)).
Take: switch auto-merge on only where a required check exists.

**GitHub Issues and Projects.** `Closes #N` closes the issue when the PR is merged into the default
branch; Projects set Done on merge or close
([docs](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)).

**Copilot coding agent.** No tracker. Its "Agent Merge" can fix failed workflows and conflicts, and
it does not remove the need for human review
([docs](https://docs.github.com/en/copilot/how-tos/copilot-on-github/use-copilot-agents/get-pull-requests-ready-to-merge)).

## Left out

- Mergify, Kodiak (label `automerge`), Tide and Homu: a cloud service or a server of its own for a
  team; for one person a status in the tracker does the same.
- bors-ng: deprecated in favour of the GitHub merge queue ([README](https://github.com/bors-ng/bors-ng)).
- GitHub merge queue: for many PRs from many people; `GITHUB_TOKEN` cannot enqueue.
- Required reviews: the author cannot approve the own PR, a dead end for one person under one token.
- Cyrus, vibe-kanban, Devin (from summaries, not read in full): worktree per issue and a PR, no
  status change after the merge described.

## Not checked

- Whether `gate` waits for every CI job; a non-required red check gives UNSTABLE and `--auto`
  merges at once.
- Whether workflows run for a PR the agent opens with the owner's token.
- OpenHands resolver, Sweep, Jira automation: no facts found.
