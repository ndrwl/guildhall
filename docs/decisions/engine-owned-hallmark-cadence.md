# Engine-owned hallmark cadence

Two things decided which hallmark was a repository's last one, and they could disagree (hallmark d78, finding H1).
The Director's hallmark cadence in `director status` counted merges from the hallmark run the Director recorded by hand with `director maintenance record --hallmark` ([Director maintenance](director-maintenance.md)).
The hallmark step's `previous` input came from the newest done task whose bound workflow has a hallmark step ([report-only workflows](report-only-workflows.md)).
A hallmark task that failed, was cancelled or never got its record left the two disagreeing: the cadence reset with no report behind it, or stayed due after a report existed, and the next hallmark was given a report the cadence did not know about.
The Guildmaster chose the report's recommended answer to its Q1: the engine owns the cadence.

This supersedes the deferred part of [report-only workflows](report-only-workflows.md) ("Not decided here", recording the maintenance run when a hallmark task starts) and the rule in [Director maintenance](director-maintenance.md) that a hallmark is recorded when its task is launched.

## Decisions

- **One selector.**
  A repository's last hallmark is its newest done task, by close receipt and then id, whose bound workflow resolves and has a step of type `hallmark` and that recorded the `finished` document and a report of a hallmark step.
  `interp.LastHallmark` is the only code that decides it; `newestHallmark` calls it excepting the task itself, for both the `previous` and `baseline` inputs, and `director status` calls it for each repository.
  It matches the step type, not the task kind or the workflow name, so a `[repository.kinds] hallmark` override still counts.
  Requiring the `finished` document makes "done hallmark" mean a hallmark the engine finished; a hallmark-only workflow always closes through `finish()`, so in practice the tasks are the ones the previous-report lookup already chose.
  A cancelled or failed hallmark is never chosen, so it never resets the cadence.
  `store.TaskCloses` gives each task's create receipt and its first close receipt and time; it is a pure read, and nothing new is stored.
- **D1. The count starts at the hallmark's launch.**
  The selector chooses the hallmark by close, but the merges counted toward the next one are those sent after the chosen task's create receipt, or after the baseline when that is later.
  This keeps the rule that merges made while a hallmark runs count toward the next one, and the switch from a launch record is nearly seamless, since a run was recorded at launch.
  Counting from the close, as the report worded it, would silently drop the merges made during a run.
- **D2. A running hallmark holds the item back.**
  Without the launch record, the item would stay due until the hallmark finished and the Director could launch a duplicate.
  While a hallmark task of the repository is open (`interp.OpenHallmark`), status names it as `running_task` and lists no `hallmark due` item; a failed or cancelled hallmark is no longer open, so the item comes back.
  The item's command is now `guildhall task new --repo <r> --kind hallmark --title <text> <id>`.
- **D3. Old runs are the fallback only for a repository with no selected hallmark.**
  Hallmark runs recorded before still decode, list and count, but only for a repository with no done hallmark task; a run newer than the newest done hallmark task is then ignored for that repository.
  With D1 the two counting points are almost the same, since a run was recorded at launch.
- **D4. With no config, status resolves against the shipped workflows by digest.**
  With a readable config, status resolves each task's workflow with `boundWorkflow` and the repository's settings, as the engine does.
  With no config, one that cannot be read or one that does not name the repository, it matches the task's revision against the shipped definitions and their retired revisions only; a hallmark of a Director-local workflow, or of one with workflow overrides, is then passed over and its repository counts from runs. `--copy` always supplies the config.
- **D5. The record is retired in the same change.**
  Once the selector decides the cadence, `record --hallmark` would be ignored by every repository with a done hallmark, a dead third ledger.
  `director maintenance record --hallmark` is a usage error, "hallmarks are recorded when the task finishes", and `store.RecordMaintenance` refuses kind `hallmark` with `ErrInvalid` and the same reason, so no writer remains.
  Old runs remain as the fallback, so the report's two-step deletion gate buys nothing here.

## Not changed

`finish()` still sends no `merged` message and writes the `finished` document as before; the baseline, the note audit and its `--notes` record are unchanged; the `previous-<step>` copy is still made once on attempt 1, reused on retries and never moves.
Deleting the old-run read once no repository depends on it is left for later.
