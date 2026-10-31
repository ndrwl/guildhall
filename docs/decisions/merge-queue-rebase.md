---
status: accepted
date: 2026-10-29
topic: delivery
tasks: d23
amends: delivery-stage.md
formerly: pr-conflict-director-control.md
---

# Authorized pull requests wait in a merge queue, and Guildhall rebases the front itself in three tiers

## Context

d11, d13, d17, d6 and d16 each had to be rebased one at a time under strict "up to date" protection and squash merges, while Guildhall only observed mergeability and left recovery to the Director (see History).
Every rebase cost a build send-back, a review, a CI run and a new merge decision.
The Guildmaster asked for a lighter flow and answered nine questions in the d23 grill (Q1 to Q9), then two more in the plan's acceptance (accept-1).

## Decision

- **A merge queue per repository, Guildhall's own (Q2, Q3).**
  A pull request is ready once its merge is authorized: the merge decision answered `merge`, or `<deliver>-<k>-authorized` recorded.
  Deliver authorizes whatever the mergeability, and only the front is brought up to date and merged ([the merge queue](../interpreter.md#the-merge-queue)).
- **Order (Q4, Q5).**
  Task priority (`p0 urgent` to `p5 someday`, `p3 normal` by default, a store record of its own: [priority](../tasks.md#priority)), then ready time, then task id.
  The front is claimed in one store mutation and kept until the task merges, is held, sent back or cancelled; a priority change reorders only the tasks behind it.
- **Ready time (accept-1).**
  The commit time of the receipt that first authorized the merge, never wall-clock time; a major send-back and `task hold-merge` keep it, so a task that keeps conflicting rejoins at its first place and is not starved.
- **Guildhall classifies the rebase itself (Q1)**, with no Director turn, by replaying the task's whole change as one commit onto the base tip mergeability read, with `git merge-tree --write-tree`: `no_conflicts` when `git patch-id --stable` is unchanged, `minor_conflicts` within `minor_conflict_files` (default 3) and `minor_conflict_lines` (default 50) with markers on every conflict (or a clean replay whose patch-id differs), and `major_conflicts` otherwise ([tiered rebase](../interpreter.md#tiered-rebase)).
- **Three tiers.** `no_conflicts` pushes, waits for CI and merges, the patch-id equality standing in for a new review; `minor_conflicts` has a rebaser Artisan resolve the conflicts and a narrowed review read only `diff marked..resolved`; `major_conflicts`, a failed rebaser, a behaviour change, red CI or narrowed-review changes send the build back automatically, with feedback naming the base, the files and the reason.
- **The rebaser is declared, not built in (Q8).**
  It runs under the copy's profile named `rebaser` and reads the conflicting files, the commits on both sides that touch them and the whole accepted spec; a copy without the profile escalates to `major_conflicts` and says why.
- **Approval carries over under a threshold (Q7c).** `[repository.merge] reapprove_after_rebase`, default `major_conflicts`, orders `no_conflicts` < `minor_conflicts` < `major_conflicts` < `never` ([rebase settings](../configuration.md#rebase-settings)); below it the approval carries over as `guildhall.merge-authorization/2`, only while every gate holds, `nothing_open` holds with no new findings and the accepted spec is unchanged. `never` is refused unless `artisan_approval` is required, and findings and red CI always reach the Guildmaster.
- **Three rounds per stay.**
  After three rebases in one stay at the front the task waits for the Director with `rebase-stuck`.
- **Hold and visibility (Q9).** `task hold-merge` toggles a recorded hold that keeps the authorization and ready time but is skipped by the queue; it is not the `hold` answer of a merge decision.
  The queues show in `director status --json` and `task list --json`, and a paused Director pauses the queue.
- **The validate exception (accept-1).**
  GitHub runs no `pull_request` checks on a conflicting pull request, so validate runs the same tiered rebase on one and reads the rebased head's checks; no approval exists yet, so no threshold applies.
  It is the only rebase of a pull request that is not at the front.

## Alternatives rejected

- GitHub's merge queue: left to d24.
- `git rebase`: squash merges make the branch's own history irrelevant, and one replayed commit gives one conflict round that matches the measurement.
- Leaving every rebase to the Director, as first decided: each cost a full build, review and CI round.
- Ordering attempt starts by priority (Q6): split out to d25, now part of [guard rails](guard-rails.md).

## Consequences

- R1.36: a head Guildhall rebased is covered by its recorded rebase, patch equivalence or a clean narrowed review, with the approval of the head it replaced.
  R1.58, R1.63 and the [rails-not-tracks](rails-not-tracks.md) invariants: changing the head invalidates stale evidence unless a recorded rebase proves coverage, and the Director still cannot grant either.
- `internal/workflow/default.json`, step ports and open tasks' workflow digests are unchanged: the rebaser and narrowed review are synthetic steps `<step>-rebase` and `<step>-rebase-review`, which still count against the attempt caps.
- `git.Program.Classify`, the rebase-aware review of a new build head, `--match-head-commit` and the hard `artisan_approval` and `ci_green` gates are unchanged; a rebuilt build head still gets a new review round.
- `step send-back` and the `pr-blocked` observation records stay, for a `rebase-stuck` front or a copy that cannot rebase.
- A pull request only behind at validate reads its checks unrebased, and one further back in the queue is never rebased.

## History

- 2026-10-26, pr-conflict-director-control.md: Guildhall observed mergeability but never rebased.
  Validate and deliver reported a conflicting or protection-blocked behind pull request as a durable `pr-blocked` document and inbox message, deduplicated by head and state, naming the pull request, head, base, behind count and conflicting paths, and offering send-back, hold and abandon as independent options rather than a recovery procedure; unknown mergeability stayed an ordinary wait rather than an assumed conflict, and the base tip was read with `git ls-remote` and fetched exactly without changing a worktree.
  It also added `step send-back`: it names a succeeded Artisan step and attempt, requires Director feedback recorded immutably before the new attempt, replays without starting another, refuses changed feedback and any request while an attempt runs, and is not a retry of a failed attempt.
  This record superseded the rule that the Director owns mergeability on 2026-10-29, because each rebase cost a full round; send-back and the observation records are unchanged.
