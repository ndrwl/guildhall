# Report-only workflows: the hallmark task kind

A hallmark reviews the whole repository and writes ranked, PR-sized follow-ups without changing it ([skill](../../skills/guildhall-hallmark/SKILL.md)).
It is launched as a `hallmark` [task kind](task-kinds.md), `guildhall task new --kind hallmark`, which runs the shipped `hallmark` workflow, or the workflow a repository names in `[repository.kinds] hallmark`.
That workflow has one step, `hallmark`, of the new `hallmark` step kind: Artisan-only, optional single `previous` and `baseline` document inputs, a `report` document output and the settings `profile`, `skills` and `instructions`.
The shipped step runs under the `researcher` profile with the `guildhall-hallmark` skill.
A `research` step could not carry the skill, and widening `research` for every workflow was rejected; a kind of its own also gives the engine a type to key the read-only check and the previous report on.

## A workflow without deliver finishes as done

Until now the engine refused a workflow that ran every step without waiting, and only deliver, or a merge outside Guildhall, closed a task.
When every step is done and the bound definition has no `deliver` step, the advance now records the `finished` document, sends the Director a `finished` message naming the last step's output, marks the task `done` and waits `done`.
It sends no `merged` message, so a hallmark never counts toward the next hallmark's cadence, and it opens no Guildmaster audit, which is bound to a merged head.
When the last step is a hallmark step that recorded a report, the close opens the Guildmaster's `follow-ups` decision first (see below), so a close that fails replays to the same decision.
A definition that has a deliver step keeps the old refusal if it ever runs past it, so no existing workflow changes behaviour.
The `finished` document names, for a workflow whose every attempted step is a hallmark step, the commit the last attempt was prepared at; prune retires such a task's worktree at that head as it does at a merge record's, and keeps any other deliver-less task's worktree.

## Read-only is enforced at intake

Every Artisan runs with edits allowed, since it writes its result files into the work directory, so a grant cannot make a stage read-only.
Instead, the launch of a hallmark attempt records the worktree's HEAD as `<step>-<n>-prepared`, and intake refuses its result, as the result's fault, when the worktree has a staged or unstaged tracked change or HEAD is no longer that commit.
The refusal fails the attempt and says "hallmarks must not change the project"; untracked files, the report and test caches among them, are allowed, and tracked files a test or build changed count as changes.
Running the test suite needs a per-repository grant for the researcher profile (`[repository.profiles.researcher]`); toolchain commands stay out of shipped grants.

## The previous report is copied once

The first attempt of a hallmark step looks for the newest done task of the repository, by close time, whose bound workflow has a `hallmark` step and recorded a report, and, since [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md), its `finished` document (`interp.LastHallmark`, which the Director's cadence reads too), and copies that report into the task's documents once as `previous-<step>`, given as the `previous` input.
It matches a hallmark step rather than a workflow name, so a `[repository.kinds] hallmark` override still continues the ledger.
Later attempts reuse the copy, and every attempt's binding records it, so a hallmark that finishes later never changes what an earlier attempt was given.
With no such task the input is absent, and the stage says the report is the repository's first recorded hallmark.
Hallmarks from before Guildhall recorded them are not imported; the Director may attach one as task context.

Addendum (d81): evidence reaches a hallmark by input, not by grants. The engine renders a `baseline` document once per task, with the previous hallmark's head, the current main, the merges between them and their check runs, and `guildhall ci log` serves the logs of the failed checks it lists ([interpreter](../interpreter.md#report-only-workflows)); no toolchain or CI grant is added to a shipped profile.

## Authority and overrides

The `hallmark` workflow ships release-approved beside `default` and `bug`.
Because it has no guarded step, the workflow-edit authority could have admitted any revision that meets the floor as a non-weakening change of it, so a `publish` or `deliver` step a baseline has none of now counts as weakening.
A repository's `workflow_overrides` are keyed by step id across every workflow its kinds run, so a workflow other than the feature workflow takes only the overrides of the steps it has; the feature workflow still refuses an override naming a step it lacks.

## The Guildmaster picks the follow-ups

A finished hallmark's close opens one decision, id and kind `follow-ups`, bound to the report its latest attempt recorded as a document subject, so an answer applies to that exact report.
Its question names the repository, the task and the report's absolute path, lists the report's `### H<n>. <title>` headings in report order, and gives the answer format.
The headings are best effort: a report with none is named by its path only, a list too long for the question is cut and ends with `… see the report`, and parsing never fails the close.
It offers `pick` and `none`; a `pick` answer gives the picked ids on the following lines, one per line or comma-separated, so the answer's choice stays countable while the ids stay free text.
The decision is announced as `decision.follow-ups` and the `finished` message says it is open; `director status` shows it as a `decision` item of the done task.
Answering it records the answer only: Guildhall creates no task, and the Director runs `guildhall task new` for each picked id and nothing else.
A refused or failed hallmark attempt opens no decision, and prune does not wait for an open `follow-ups` decision, since the decision and its report are store records that outlive the worktree.

## Not decided here

How the last hallmark run is recorded is not decided here: [engine-owned hallmark cadence](engine-owned-hallmark-cadence.md) decides it, reading the repository's last done hallmark task, so creating or starting a hallmark task records nothing and `director maintenance record --hallmark` is refused.
