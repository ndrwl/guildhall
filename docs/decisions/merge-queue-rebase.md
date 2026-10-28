# Authorized pull requests wait in a merge queue, and Guildhall rebases the front itself

This supersedes [pull-request mergeability belongs to the Director](pr-conflict-director-control.md) and amends the "Left out" of [merge requirements](merge-requires.md).
Requirements R1.36, R1.58 and R1.63 and the [rails-not-tracks](rails-not-tracks.md) invariants now let a recorded rebase cover a rebased head.

## Context

d11, d13, d17, d6 and d16 each had to be rebased one at a time under strict "up to date" protection and squash merges.
Every rebase cost a build send-back, a review, a CI run and a new merge decision.
The Guildmaster asked for a lighter flow and answered nine questions in the d23 grill (Q1–Q9), then two more in the plan's acceptance (accept-1).

## Decision

- **Q2, Q3: a merge queue per repository, Guildhall's own.**
  A pull request is ready once its merge is authorized: the merge decision answered `merge`, or `<deliver>-<k>-authorized` recorded.
  Ready pull requests wait in their repository's queue, and only the front is brought up to date and merged.
  Deliver opens the merge decision, or records the authorization, even when the pull request is behind or conflicting; nothing waits for the Director before the decision any more.
  GitHub's merge queue is left to d24.
- **Q4: order.** The queue is ordered by task priority, then ready time, then task id.
  The front is claimed in one store mutation, so two tasks advancing at once cannot both act as the front.
  A task keeps the front until it merges, is held, is sent back or is cancelled; a priority change reorders only the tasks behind it, and nothing is preempted.
- **Q5, Q5b: priority.** Each task has a priority, `p0 urgent` to `p5 someday`, `p3 normal` by default ([priority](../tasks.md#priority)).
  It is a store record of its own, set with `task new --priority` and `task priority <task> <level>`, shown in `task list`, `task show` and the timeline, and set by the Director from the Guildmaster's words.
- **Q6** (the highest-priority waiting attempt starts first when the attempt caps are full) is split out into task d25; d23 orders only the merge queue.
- **The ready time is the first one (accept-1, change 1).**
  It is the commit time of the receipt that first authorized the task's merge, never wall-clock time.
  A major send-back takes the task out of the queue but keeps its ready time, so a task that keeps conflicting rejoins at its first place and is not starved.
  `task hold-merge` keeps it too; only a merge or a cancellation drops it.
- **Q1: Guildhall classifies the rebase itself**, with no Director turn, from conflicts measured against the base tip that mergeability read.
  A rebase replays the task's whole change as one commit onto that tip with `git merge-tree --write-tree`, never with `git rebase`; squash merges make the branch's own history irrelevant.
  Conflicting lines are the lines between `<<<<<<<` and `>>>>>>>` on both sides, without the marker lines.
  - `no_conflicts`: no conflicts, and `git patch-id --stable` of the reviewed change equals that of the rebased one.
  - `minor_conflicts`: at most `minor_conflict_files` files (default 3) and `minor_conflict_lines` lines (default 50), every one with markers; also a clean replay whose patch-id differs, reviewed as a whole.
  - `major_conflicts`: anything else, including a conflict without markers (binary, delete/modify, rename).
- **The three tiers.**
  - `no_conflicts`: Guildhall pushes the rebased head to the same pull request, waits for its CI and merges it; the patch-id equality stands in for a new review.
  - `minor_conflicts`: a cheap rebaser Artisan resolves the conflicts, and a narrowed review reads only the resolved hunks, `diff marked..resolved`; then push, CI and merge as for `no_conflicts`.
  - `major_conflicts`, a rebaser that fails or reports a behaviour change, red CI on the rebased head or narrowed-review changes: today's build send-back, now automatic, with feedback naming the base, the conflicting files and the reason.
- **Q8: the rebaser is declared, not built in.** It runs under the copy's profile named `rebaser`; the example config gives it a small model, low effort and git and go tools only.
  A copy without that profile escalates minor conflicts to `major_conflicts` and says why.
  The rebaser reads the conflicting files with markers, the commits on both sides that touch them, and the whole accepted spec (accept-1), since specs have no fixed goal heading.
  If it cannot resolve the conflicts without changing behaviour it says so, and Guildhall escalates.
- **Q7c: approval carries over under a threshold.**
  `[repository.merge] reapprove_after_rebase`, default `major_conflicts`, orders `no_conflicts` < `minor_conflicts` < `major_conflicts` < `never`.
  A rebase at or above it needs a new merge decision; below it, the earlier approval carries over as `<deliver>-<k>-authorized`, schema `guildhall.merge-authorization/2`, naming what it carried and the rebase chain.
  It carries over only while every gate holds for the rebased head, `nothing_open` holds with no new findings, and the accepted spec is the one the original authorization was given under.
  `never` never asks, and carries the authorization over a major send-back to the rebuilt head; it is refused at config parse unless `artisan_approval` is required (accept-1).
  Findings and red CI always reach the Guildmaster, whatever the threshold.
- **Three rounds per stay.** If the base moves again while the front waits, it rebases again; after three rebases in one stay at the front it waits for the Director with `rebase-stuck`.
- **Q9: hold and visibility.** `task hold-merge <task>` toggles a recorded hold: the task stays authorized and keeps its ready time, but the queue skips it, and a held front releases the front.
  It is not the `hold` answer on a merge decision, which leaves the task waiting for the Director.
  `director status --json` gains `merge_queues` and `task list --json` a `merge_queue` object per queued task, plain JSON for later chat commands.
  A paused Director pauses the queue: the supervisor's merge-queue watcher runs only while the session is running or recovering.
- **The validate exception (accept-1, change 2).**
  GitHub runs no `pull_request` checks on a conflicting pull request, so one that conflicts before it is ready could never become ready.
  Validate therefore runs the same tiered rebase on a conflicting pull request, onto the base tip mergeability read, publishes the rebased head to the same pull request and reads its checks; its result is bound to the rebased head and names the rebase chain.
  There is no approval yet, so the threshold does not apply, and the later merge decision is for the rebased head.
  `major_conflicts`, a behaviour change, red CI or narrowed-review findings take validate's existing `failed` loop back to build, with the same feedback.
  This is the only rebase of a pull request that is not at the front: a pull request that is only behind at validate reads its checks as usual, and one further back in the queue is never rebased.

## Requirement changes

- **R1.36:** a head Guildhall rebased is covered by its recorded rebase, tier-1 patch equivalence or a clean narrowed review of the resolved hunks, together with the approval of the head it replaced.
- **R1.58, R1.63 and the rails-not-tracks invariants:** changing the head invalidates stale evidence unless a recorded rebase proves coverage; merge approval carries over only under the repository's `reapprove_after_rebase`; the Director still cannot grant either.

## What stays

- `internal/workflow/default.json`, every step kind's ports and every open task's workflow digest: the queue and the rebase run inside deliver and validate, and the rebaser and narrowed review are synthetic steps `<step>-rebase` and `<step>-rebase-review`, not workflow steps.
  Both count against the attempt caps.
- `git.Program.Classify` and the rebase-aware review of a new build head.
- `--match-head-commit` merges, and a required `artisan_approval` or `ci_green` that is never merged over.
- Repositories with `guildmaster_approval = false` behave as before, apart from queueing and the tiered rebase.
- `step send-back` is still the Director's, for a `rebase-stuck` front or a copy that cannot rebase, beside holding the task or leaving it unadvanced.

See [the merge queue](../interpreter.md#the-merge-queue) and [tiered rebase](../interpreter.md#tiered-rebase) for the records and the order deliver and validate follow.
