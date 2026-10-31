---
status: accepted
date: 2026-10-30
topic: tasks
tasks: d47
amends: worktree-custody.md
amended-by: multi-pr-tasks.md
---

# A task can be cancelled for good, with a reason, voiding its decisions and closing its pull requests

## Context

Guildhall had no way to end a task without delivering it, so dropped tasks sat in every list with open decisions or parked attempts.

## Decision

`guildhall task cancel --repo <repo> <task> --reason <text>` ends an open task for good, at the Guildmaster's request ([task cancel](../tasks.md#task-cancel)).

- **One command, reason required, irreversible.**
  A cancelled task is never advanced, judged, audited or un-cancelled; wanted work becomes a new task.
- **A separate store operation.**
  `CancelTask(mutation, repo, task, reason)` sets `cancelled`, `cancel_reason` and `cancelled_at`; its receipt counts toward `handoff_after_tasks` as `set-task-state` does.
- **Void, not answered.**
  `VoidDecision` marks an unanswered decision `void` with a reason, and `Decision.Open` (neither answered nor void) replaces `!Answered` wherever a decision is tested as waiting: `director status`, the dependency gate, `decide list` and the merge queue's front wait.
  `AnswerDecision` refuses a void decision, and `decide answer` refuses a cancelled task's decisions.
- **Every recorded pull request is closed**, read from the task's `-pr` documents once per remote and number, with the forge's [`Close`](../contracts/forge.md): a comment giving the reason, never `--delete-branch`; an already closed pull request is success and a merged one `ErrRefused`.
- **Order: stop, close, then the records.**
  A `-merge` document refuses before anything; stops come first, and a stop `launch.Stop` cannot prove (`ErrNotStopped`) refuses the cancel with nothing else changed, keeping the invariant that a cancelled task has no live Artisan.
  A Director stage's attempt has no owner and is recorded `stopped`.
  The state change is last, and every write has a mutation id `cancel.<repo>.<task>.<part>`, so a cancel that fails part-way is finished by running it again.
- **Dependents are told, not cancelled**: one `dependency-cancelled` message per open dependent in any listed repository, id `<repo>.<task>.dependency-cancelled.<cancelled repo>.<cancelled task>`.
- **A cancelled task's advance succeeds quietly.** `task start` and `step done` of a cancelled task print `task <repo>/<task> is cancelled; nothing to advance` and exit 0, whatever the order of the cancel and the launcher's advance, since stopping an owned attempt makes its launcher run `task start`; a done task's advance still fails.
- **Prune**, which d46 had just added, selects `cancelled` tasks as it selects `done` ones, `prune_after` after the `cancel-task` receipt, and a void `audit` no longer holds a task.
  Attempt files are pruned; the worktree and branch are kept, with `the task was cancelled unmerged, …` as the reason, the daily prune tells the Director once, and the Director decides.

## Alternatives rejected

- A reason on `SetTaskState`'s request: it would change the request digest of every `done` close and deliver's replay.
- Refusing `decide answer` for every closed task, as the spec said: a done task's `audit` decision is answered after it merged ([quality](../quality.md)).
- The spec's order, the merged-pull-request refusal first and the close after voiding decisions and leaving the queues: the forge has no read of a pull request's state besides `Close`, so a pull request merged on the forge would refuse the cancel only after decisions were voided, stranding an open task with decisions no one could answer.
- Closing only the latest pull request: publish rounds and rebase links normally name the same one, and closing a closed one changes nothing, so no notion of "latest" is needed.
- Removing unmerged work at prune: it was not specified, and `Retire` removes a worktree only at the head a `-merge` document names.

## Consequences

A cancelled task's unread messages stay in `director status`'s `messages` item until read (open question 1, accepted).
The dependency gate's own `dependency-held` text and ids are unchanged, and a re-run resends no `dependency-cancelled` message.
The launcher records the attempt's outcome before advancing, so nothing is lost when the advance prints that the task is cancelled.
Cancelling is another way a task closes besides merge, so [worktree custody](worktree-custody.md)'s "nothing cancels a task yet" no longer holds; the worktree is still kept.
