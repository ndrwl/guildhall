---
status: accepted
date: 2026-10-31
topic: maintenance
tasks: d80, d78, d119, d121
amends: director-maintenance.md, report-only-workflows.md
amended-by: closed-tasks-have-no-open-decisions.md
---

# A repository's last hallmark is its last done hallmark task, chosen by one engine selector

## Context

Two things decided which hallmark was a repository's last one, and they could disagree (hallmark d78, finding H1).
The Director's hallmark cadence in `director status` counted merges from the hallmark run the Director recorded by hand with `director maintenance record --hallmark` ([Director maintenance](director-maintenance.md)).
The hallmark step's `previous` input came from the newest done task whose bound workflow has a hallmark step ([report-only workflows](report-only-workflows.md)).
A hallmark task that failed, was cancelled or never got its record left the two disagreeing: the cadence reset with no report behind it, or stayed due after a report existed, and the next hallmark was given a report the cadence did not know about.
The Guildmaster chose the report's recommended answer to its Q1: the engine owns the cadence.

## Decision

This replaces the rule in [Director maintenance](director-maintenance.md) that a hallmark is recorded when its task is launched, and decides the part [report-only workflows](report-only-workflows.md) left open ("Not decided here").

- **One selector.**
  A repository's last hallmark is its newest done task, by close receipt and then id, whose bound workflow resolves and has a step of type `hallmark` and that recorded the `finished` document and a report of a hallmark step.
  `interp.LastHallmark` is the only code that decides it, for the `previous` and `baseline` inputs and for `director status`.
  It matches the step type, not the task kind or workflow name, so a `[repository.kinds] hallmark` override still counts; a cancelled or failed hallmark is never chosen, so it never resets the cadence.
  `store.TaskCloses` gives each task's create receipt and first close; it is a pure read, and nothing new is stored.
- **The count starts at the answer to the hallmark's follow-ups (d119).**
  Merges counted toward the next hallmark are those sent after the receipt answering the chosen task's `follow-ups` decision, `pick` or `none` alike, or after the baseline when that is later, so merges made during the run or before the answer do not count.
  A chosen task with no `follow-ups` decision (finished before it existed, or whose report could not be read back) counts from its create receipt, its launch, as before; so does a void one, so it cannot hold the item forever.
  Since d121 a hallmark closes only once its `follow-ups` decision is answered, so the chosen task's answer always precedes its close; while a hallmark waits on it, it is the open hallmark and the previous hallmark is still the chosen one, so `merged_since` counts from that one's answer.
  The answer receipt comes from the Director view's receipts, with no new record or read.
- **A running hallmark holds the item back.**
  While a hallmark task of the repository is open (`interp.OpenHallmark`), status names it as `running_task` and lists no `hallmark due` item, so the Director cannot launch a duplicate.
  A hallmark waiting on its unanswered `follow-ups` decision, deferred or not, is still open ([closed tasks have no open decisions](closed-tasks-have-no-open-decisions.md)), so this hold also keeps a new hallmark from being queued before the Guildmaster has given feedback on the last one.
  d119's separate `awaiting_follow_ups` status field, which named a done hallmark with its decision open, could no longer be set once the hallmark stays open, and d121 removed it.
  The item's command is now `guildhall task new --repo <r> --kind hallmark --title <text> <id>`.
- **Old runs are the fallback only for a repository with no selected hallmark.**
  Hallmark runs recorded before still decode, list and count, but only for a repository with no done hallmark task; a run newer than the newest done hallmark task is then ignored.
- **With no config, status resolves against the shipped workflows by digest.**
  With a readable config, status resolves each task's workflow with `boundWorkflow` and the repository's settings, as the engine does; otherwise it matches the task's revision against the shipped definitions and their retired revisions only, so a hallmark of a Director-local or overridden workflow is passed over and its repository counts from runs.
  `--copy` always supplies the config.
- **The record is retired in the same change.**
  `director maintenance record --hallmark` is a usage error, "hallmarks are recorded when the task finishes", and `store.RecordMaintenance` refuses kind `hallmark` with `ErrInvalid`, so no writer remains.

## Alternatives rejected

- Counting from the hallmark's close, as the report worded it: it would silently drop the merges made during a run.
  d119 drops them on purpose, and more: the Guildmaster saw hallmark reports overlap (d107, then d118) and wants the cadence paced by feedback, not by launch, so merges count only after the follow-ups answer.
- Letting a deferred follow-ups decision release the item: a deferral does not answer it, so a deferred decision can hold hallmarks indefinitely; that is chosen, since a hallmark nobody has read the last report of is wasted.
- Keeping `record --hallmark`: every repository with a done hallmark would ignore it, a dead third ledger.
- The report's two-step deletion gate: old runs remain as the fallback, so it buys nothing.

## Consequences

Requiring the `finished` document makes "done hallmark" mean one the engine finished; a hallmark-only workflow always closes through `finish()`, so in practice these are the tasks the previous-report lookup already chose, and the switch from a launch record is nearly seamless.
A failed or cancelled hallmark is no longer open, so the item comes back.
`finish()` still sends no `merged` message; the baseline, the note audit and its `--notes` record are unchanged; the `previous-<step>` copy is still made once on attempt 1, reused on retries and never moves.
Deleting the old-run read once no repository depends on it is left for later.
