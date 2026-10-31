---
status: accepted
date: 2026-10-31
topic: workflow
tasks: d74, d77, d81
amended-by: engine-owned-hallmark-cadence.md
---

# A report-only workflow finishes as done without delivering, and the hallmark task kind runs one

## Context

A hallmark reviews the whole repository and writes ranked, PR-sized follow-ups without changing it ([skill](../../skills/guildhall-hallmark/SKILL.md)).
Until then the engine refused a workflow that ran every step without waiting, and only deliver, or a merge outside Guildhall, closed a task.

## Decision

A hallmark is a `hallmark` [task kind](task-kinds.md), `guildhall task new --kind hallmark`, running the shipped `hallmark` workflow or the one `[repository.kinds] hallmark` names.
That workflow has one step of the new `hallmark` step kind: Artisan-only, optional single `previous` and `baseline` document inputs, a `report` document output and the settings `profile`, `skills` and `instructions`; the shipped step runs under `researcher` with the `guildhall-hallmark` skill.

When every step is done and the bound definition has no `deliver` step, the advance records the `finished` document, sends the Director a `finished` message naming the last step's output, marks the task `done` and waits `done` ([report-only workflows](../interpreter.md#report-only-workflows)).
It sends no `merged` message, so a hallmark never counts toward the next hallmark's cadence, and opens no Guildmaster audit, which is bound to a merged head.
A definition with a deliver step keeps the old refusal if it ever runs past it, so no existing workflow changes behaviour.
For a workflow whose every attempted step is a hallmark step, `finished` names the last attempt's prepared commit, at which prune retires the worktree as at a merge record's head; any other deliver-less task's worktree is kept.

Read-only is enforced at intake: launching a hallmark attempt records the worktree's HEAD as `<step>-<n>-prepared`, and intake refuses the result, as its fault, when the worktree has a staged or unstaged tracked change or HEAD moved, saying "hallmarks must not change the project"; untracked files, the report and test caches among them, are allowed.
The first attempt copies the report of the repository's last hallmark, chosen as [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md) chooses it, once into the task's documents as `previous-<step>`; later attempts reuse the copy and every binding records it, so a hallmark that finishes later never changes what an earlier attempt was given, and with none the stage says the report is the repository's first.
Evidence reaches a hallmark by input, not by grants (d81): the engine renders a `baseline` document once per task, with the previous hallmark's head, the current main, the merges between them and their check runs, and `guildhall ci log` serves the failed checks' logs.

The `hallmark` workflow ships release-approved beside `default` and `bug`; because it has no guarded step, a `publish` or `deliver` step a baseline has none of counts as weakening, so the authority cannot admit any revision meeting the floor as non-weakening.
A repository's `workflow_overrides` are keyed by step id across every workflow its kinds run, so a workflow other than the feature workflow takes only the overrides of the steps it has; the feature workflow still refuses an override naming a step it lacks.

A finished hallmark's close opens one decision, id and kind `follow-ups`, bound to its latest attempt's report as a document subject, before the `finished` message, so a close that fails replays to the same decision (d77).
Its question names the repository, task and report path and lists the report's `### H<n>. <title>` headings best effort, never failing the close; it offers `pick`, with the picked ids as free text on the following lines, and `none`.
Answering it records the answer only: Guildhall creates no task, and the Director runs `guildhall task new` for each picked id and nothing else.

## Alternatives rejected

- A `research` step carrying the hallmark skill: research refuses `skills`, and widening it for every workflow was rejected; a kind of its own gives the engine a type to key the read-only check and the previous report on.
- Making a stage read-only by grant: every Artisan writes its result files into the work directory, so it runs with edits allowed.
- Toolchain or CI grants on a shipped profile for the hallmark's evidence: the baseline input gives it instead, and running the test suite needs a per-repository grant for `researcher`.
- Guildhall creating the picked follow-up tasks: the Guildmaster picks, and the Director creates them.

## Consequences

Tracked files that a test or build changes count as changes and fail the attempt.
The previous report matches a hallmark step, not a workflow name, so an override still continues the ledger; hallmarks from before Guildhall recorded them are not imported, though the Director may attach one as task context.
A refused or failed hallmark attempt opens no `follow-ups` decision, and prune does not wait for an open one, since the decision and its report are store records that outlive the worktree.
This record left open how the last hallmark run is recorded; [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md) decided it from the repository's last done hallmark task, so creating or starting a hallmark task records nothing and `director maintenance record --hallmark` is refused.
