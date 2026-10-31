---
status: accepted
date: 2026-10-31
topic: tasks
tasks: d121
amends: report-only-workflows.md, engine-owned-hallmark-cadence.md, director-maintenance.md, retire-quality-audit.md
---

# A closed task never has an open decision, and a hallmark stays open until its follow-ups are answered

## Context

Two paths left a closed task with an open decision.
A hallmark closed itself done and then left its `follow-ups` decision open ([report-only workflows](report-only-workflows.md)), so d119 had to add a separate `awaiting_follow_ups` hold to keep the next hallmark back until the answer ([engine-owned hallmark cadence](engine-owned-hallmark-cadence.md)).
The last Guildhall merge closed a task without voiding anything, though a `merge` answer can override a failing `nothing_open` with a question, a loop exit or an earlier round still open.
Cancel and the outside-merge rule voided open decisions, the latter, and a slice's merge, except an `audit` an earlier release opened ([retired audit](retire-quality-audit.md)).
The Guildmaster asked (d121): "Can we keep the hallmark open until the follow up are decided?", then "yes to all 4".

## Decision

- **A closed task never has an open decision, with no exceptions.**
  Whenever a task closes, done, cancelled, merged outside Guildhall or by its last slice, every decision still open on it is voided first.
  The engine's closes, the last Guildhall merge, the outside-merge rule and the report-only finish, all take one path, `closeTask`, which voids each open decision under a mutation id keyed on the decision and the close, with the close's one reason, then sets the task done; a close that fails part-way replays to the same writes.
  `task cancel` already voided every open decision and is unchanged.
  A slice merge that leaves the task open voids every open decision too, the `audit` included.
  A Director's `question` decision ([decide open](../decisions-cli.md#decide-open)) is no exception: it never fails `nothing_open`, so Guildhall may merge with one open, and that merge's close voids it; a question that should outlive the task belongs on another task or in chat.
- **The store enforces it.**
  `SetTaskState` to done or cancelled and `CancelTask` refuse with `ErrInvalid` while the task has an open decision; the replay check runs first, so a close already applied still returns its receipt.
  `OpenDecision` refuses a task that is not open, so a closed task cannot gain one.
  The judge's attempts on a done task are unaffected; the judge opens no decision.
- **A hallmark stays open until its follow-ups are answered.**
  A hallmark task records its report and `finished` document, opens `follow-ups` and sends its `finished` message as before, then waits on the decision with the task open.
  Answering it, `pick` or `none`, then `guildhall task start --decision follow-ups` closes the task; answering alone never advances a task.
  A deferred decision is still open, so the hallmark stays open.
  A report-only task with no `follow-ups` decision, its last step not a hallmark step or its report unreadable, closes at once.
- **d119's `awaiting_follow_ups` is removed.**
  A hallmark waiting on its follow-ups is an open hallmark, so the running-hallmark hold (`running_task`) keeps the next hallmark back; merges still count from the follow-ups answer.
- **`nothing_open` counts only open decisions.**
  It tested `Answered`, so a voided decision, one an earlier slice's merge voided or a superseded acceptance, failed a later merge as unanswered; it now skips every decision that is not open.
- **A future post-merge audit keeps its task open.**
  Should a decision ever again be opened after a merge, such as an audit, its task stays open until it is answered, as a hallmark does, rather than closing with it open.

## Alternatives rejected

- Voiding inside `SetTaskState`: it would change the request, and so the replay digest, of every done close, which `FinishedTasks` counts; refusing and leaving the voids to the closer is smaller.
- Keeping `awaiting_follow_ups` for legacy done hallmarks: it would keep a field for a state the engine no longer makes.
- Excepting a legacy `audit` from the void: the Guildmaster said "no exceptions", and no code opens an audit any more.

## Consequences

Records an earlier release left, a done hallmark with its `follow-ups` decision open or a done task with an open `audit`, predate the rule and are not migrated: the store guards apply to new closes only, `decide answer` still answers such a decision, which restarts the hallmark count, and such a hallmark no longer holds the next one.
A hallmark's `last_at` is now the close after the answer, not the report time, and prune keeps its worktree until then.
`guildhall decide list` shows no open decision on a task the merge closed.
