# Tasks

A task is one unit of work in one repository a Director owns.
The `guildhall task` commands create tasks in the Director's store and read them back.
Creating a task records it and binds the exact workflow and configuration it was accepted under (R1.62); it launches nothing, calls no model and starts no step.
For decisions and their answers, see the [decision commands](decisions-cli.md); `task start`, which advances a task, is in [running a task](running.md).

## Commands

Every command takes `--store <path>`, an existing store file made by [`guildhall store init`](store.md#guildhall-store-init), or a [copy](running.md#copies) with `--copy <dir>` or `GUILDHALL_COPY`, whose `director.store` is the store and whose `config.toml` is `task new`'s `--config`.

- `guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--depends-on <repo>/<task>]... [--fixes <repo>/<task>] [--priority <level>] <task>` creates task `<task>` in repository `<id>` and prints the store's receipt as one JSON document.
- `guildhall task priority --store <path> --repo <id> <task> <level>` sets an open task's [priority](#priority) and prints the receipt.
- `guildhall task hold-merge --store <path> --repo <id> <task>` [holds or unholds](#holding-a-merge) an open task's place in its repository's merge queue and prints the receipt.
- `guildhall task show --store <path> --repo <id> <task> --json` prints `{"task": ..., "attempts": [...], "documents": [...], "decisions": [...]}` for one task.
- `guildhall task list --store <path> [--repo <id>] --json` prints a JSON array of tasks, ordered by repository and then task id, for one repository or every repository in the store.
  A task in its repository's [merge queue](interpreter.md#the-merge-queue) also has a `merge_queue` object, the entry `director status` lists ([status](director.md#status)); it is absent for a task that is not queued.
- `guildhall task timeline --store <path> --repo <id> <task> --json` and `guildhall task files --store <path> --repo <id> <task> --json` are the [inspection reads](#inspection).
- `guildhall task finding rule --store <path> --repo <id> --outcome <document> --finding <id> --move <move> --reason <text> <task>` records a [Director ruling](#director-rulings) and prints its receipt.
- `guildhall task review --full --store <path> --repo <id> <task>` records a [full-review request](#full-review) and prints its receipt.

For every command but `list`, the positional task id may appear before or after flags.
`show`, `list`, `timeline` and `files` open the store read-only and change nothing.
`--json` is required on all four, so a later human-readable format is not a breaking change.

Repository and task ids are lowercase letters, digits and hyphens, starting with a letter.
A title is non-blank UTF-8 of at most 64 KiB.

Exit codes are those of the messages commands: 0 on success, 1 when the configuration, the workflow or the store refuses or fails, and 2 on a usage error.
Errors are written to standard error and nothing is written to standard output.

## The bound workflow

`task new` loads the configuration with the [configuration loader](configuration.md), including a Steward-owned `managed.toml` beside it, and finds the repository there.
The repository's `workflow` setting, `default` when it names none, selects the effective workflow:

- `default` is the definition shipped with this Guildhall release.
  Any `workflows/default.json` beside the configuration is ignored.
- Any other name is the file `workflows/<name>.json` in the configuration file's directory, and the file's `name` must equal it.

The definition is parsed with `workflow.Parse`, the repository's `workflow_overrides` are applied with `workflow.ApplyOverrides`, and the result is checked with `workflow.Validate` (R1.59).
The effective workflow is then checked with `config.CheckArtisanAdapters`: every named profile must be declared in the loaded Director configuration, and every Artisan step must name a profile.
A profile-less Artisan is refused with the workflow and step named and a remedy to set a profile on that step in a custom workflow; no default adapter, real or fake, supplies a missing profile.
The shipped `default` workflow is profile-neutral, so select a custom workflow with explicit profiles before creating a task; the example Director config selects `workflows/profiled.json` with Claude profiles.
For deterministic tests or demos, name a profile whose `adapter` is `fake`; other actors need no Artisan profile.
A missing or unreadable file, a parse error, a refused override, a validation problem, an undeclared profile or a profile-less Artisan refuses the task with the problems named, and the store is not opened, so nothing is written.

The effective workflow must then be [authorized](configuration.md#enforcement-at-task-creation): shipped with the release, approved by the Guildmaster for its exact digest, or a change from a shipped or approved definition that meets the review floor and weakens nothing.
An unauthorized workflow refuses the task with its digest and what it weakens named, and writes nothing.
The task binds `workflow.Revision` of the overridden definition, its name and digest, so two repositories with different overrides of one workflow bind different digests.
It also binds the configuration digest: `sha256:` and the lowercase hex sha256 of the loaded configuration's canonical JSON encoding.
The digest covers the local and managed settings together and not the files' formatting; it is computed by this release, so a release that adds configuration fields may compute a different digest for the same files.
Later edits to either file, or to the workflow file, apply to new tasks only.

## Dependencies

Each `--depends-on <repo>/<task>` names a task of this Director, in any repository, that must already exist in the store.
An unknown dependency refuses the task and writes nothing; a task cannot depend on itself.
The dependency order is preserved as given.
Dependencies are fixed at creation.

A dependent task waits for its dependencies: its grill runs at once, but research, or whatever its first step not run by the Director is, does not start until every dependency is `done`, that is, merged.
That is the [dependency gate](interpreter.md#the-dependency-gate): while it holds the task, advancing it records nothing and prints `{"wait":"dependencies","step":"<step>"}`, and the Director gets one `dependency-held` message for each change in the dependencies' coarse state (in progress, waiting for the Guildmaster or the Director, cancelled), never one per advance.
The session supervisor's [dependency watcher](director.md#dependencies) advances the task once its dependencies merge, with no model turn, and research then runs on a branch fast-forwarded to the main they merged into; a branch that already has commits is not rebased then, only later by the [merge queue](interpreter.md#the-merge-queue) at the front, or at validate when its pull request conflicts ([tiered rebase](interpreter.md#tiered-rebase)).
While a dependency waits in the merge queue, its state in `dependency-held` reads `queued for merge`, or `merge held` after [`task hold-merge`](#holding-a-merge).
A task already past research, with an attempt of a step not run by the Director, is never held, so a loop, retry or send-back back to research runs while a dependency is still open.

The Director can start a held task anyway with [`task start --ignore-deps --reason <text>`](running.md#task-start), the **dependency override**.
It is recorded as its own record, never by changing the task (R1.62), lasts for the rest of the task's life, starts the task from the current main and shows with its reason in `task timeline`.
A cancelled dependency never merges, so the override is the only way to start a task that depends on one.

`--fixes <repo>/<task>` names the earlier task whose defect this one fixes, which must already exist; publish then adds "Fixes a defect introduced by task `<repo>/<task>`." to the pull request body ([quality rubric](quality.md#running-it)).

## Priority

Every task has a priority, one of six levels:

| Level | Word |
| --- | --- |
| `p0` | `urgent` |
| `p1` | `highest` |
| `p2` | `high` |
| `p3` | `normal`, the default |
| `p4` | `low` |
| `p5` | `someday` |

A level may be given as `p1`, `1` or `highest`, in any letter case; anything else is a usage error.
`task new --priority <level>` sets it when the task is created, and `task priority <task> <level>` changes it on an open task at any time.
The priority is its own record, `task-priority`, never a change to the task (R1.62); it holds the latest level and every level set, and `task timeline` shows the levels with when each was set.
`task list` and `task show` print `priority` on each task, and setting the level already in force prints the receipt that set it and records nothing.

Priority orders the repository's [merge queue](interpreter.md#the-merge-queue): a lower number comes first, and tasks of the same priority keep the order in which they first became ready to merge.
A change reorders the tasks waiting behind the front at once; it never takes the front from the task holding it.

## Holding a merge

`task hold-merge <task>` toggles a recorded hold on the task's merge, receipt record `merge-hold`: run once it holds, run again it unholds.
A held task stays authorized and keeps its ready time, but the [merge queue](interpreter.md#the-merge-queue) skips it; a held front gives up the front at its next advance, and the next task in order may claim it.
Unholding lets the task claim the front again when it is first, in a new stay with three more rebase rounds.
Every change shows in `task timeline`, and `director status` and `task list --json` show `held`.

This is not the `hold` answer on a merge decision.
That answer refuses the merge it was asked for, and the task waits for the Director; `hold-merge` acts on a merge already authorized, and the task waits in the queue.
A held task also keeps its dependents waiting, whose `dependency-held` message reads `merge held`.

## Retries

`task new` records the mutation `task:<repo>:<task>` when that string is at most 128 bytes.
For longer strings it records `task:sha256:<digest>`, using the lowercase hex SHA-256 of the full original string.
Repository and task ids cannot contain `:`, so the input is unambiguous; short ids remain unchanged and every valid pair has a bounded, deterministic mutation id.
Running the same `task new` again, for example after losing its output, prints the original receipt and writes nothing, even after a restart.
Reusing a task id with a different title, dependencies, workflow revision or configuration digest is refused with the store's mutation conflict and changes nothing.

The store has one writer at a time; each command holds it only for its own operation (see [the store](store.md#one-writer)).

## Inspection

The Director inspects a task through read-only commands rather than by opening worker files (D24).
`timeline` and `files` read only the store: one read transaction, with the store opened read-only, so they never write, never create a store, and wait for the writer's lock like any other read.
Each prints one JSON document whose `as_of` label says exactly what it describes:

- `partial`: always `true`; `excluded` lists live worker state, worker files and transcript, none of which these store-only reads inspect.
- `sequence` and `committed_at`: the newest mutation committed anywhere in the store when the read began.
  Two reads of the same store with the same sequence saw the same records.
- `workflow`: the workflow revision (`name`, `digest`) the task is bound to.

Times are commit times of the store's [receipts](store.md#mutations-and-receipts), in UTC; they record when Guildhall wrote a record, not when a worker did the work.
An unknown repository or task fails with exit 1 and the store's not-found error.

`task timeline` prints:

- `omitted_kinds`: the record kinds this version leaves out, each with why; empty now that findings and rulings are recorded.
- `task`: the task record.
- `attempts`: every stage attempt with its outcome (absent while running), `started_at`, `finished_at` and `duration_seconds` between them, ordered by when they started.
- `decisions`: every decision with its kind, subject, options, question and the Guildmaster's exact words (`answer`, `choice`, `note`), plus `opened_at`, `answered_at` and `wait_seconds` between them, ordered by when they opened.
- `findings`: every review outcome document with its findings (`id`, `rule`, `file`, `line`, `summary`) and its `dispositions` of the previous round's findings, read from the store's projection, with `recorded_at`, ordered by when they were recorded.
- `rulings`: every [Director ruling](#director-rulings) with its document, finding, `move` and `reason`, `contested` and `contested_by`, and `ruled_at` and `contested_at`, ordered by when they were made.
- `full_reviews`: every [full-review request](#full-review) with its `number`, `consumed_by` (absent while pending), `requested_at` and `consumed_at`, in number order.
- `dependency_override`: the Director's [dependency override](#dependencies), with its `reason` and `overridden_at`; absent when the task has none.
- `events`: every receipt of the task in commit order, each naming its operation, mutation id, sequence, commit time and the record it wrote (`record`, for example `attempt/build/2`, `decision/accept-plan` or `message/q-1`).
  These are the moves made on the task, including messages sent and acknowledged for it.

`task files` prints `documents`: every document reference of the task with its relative path, its recorded `sha256` and `recorded_at`, in name order.
The digest is the one recorded when the reference was written; `files` does not open or re-hash the document.

Receipts committed before receipts named their record have no `record`; they still appear in `events`, but cannot supply record timestamps.
Unknown timestamps are omitted from the JSON.
Timestamps are matched by operation: an attempt finished or a decision answered after an upgrade can have a known completion time with an unknown start.
Durations require both endpoints; attempts and decisions without a start time follow the timed ones in store order.
The timeline includes every recorded event kind in commit order.
Findings come from the projection the engine records with each review outcome document, so the timeline never opens the outcome file.
Reading a task's diff, process status or transcript needs its worktree and adapter, and is separate work.

## Director rulings

The Director may rule on one finding of a review outcome document instead of waiting for the reviewer (report §5.4).
`task finding rule` records the ruling with the mutation `ruling:<repo>:<task>:<document>:<finding>`, hashed as for `task new` when longer than 128 bytes, so a retry prints the original receipt.
`--outcome` names the outcome document as the timeline's `findings` show it, such as `review-2-outcome`, and `--finding` one of its finding ids.
`--move` is one of:

- `out-of-scope`: the finding is outside the accepted spec; the reason cites the spec section;
- `duplicate`: the finding repeats another one;
- `defer`: the finding moves to a follow-up task; only a finding that cites no rule may be deferred.

A finding is ruled on once per document; a conflicting retry, an unknown document or finding, a whitespace-only reason, an unknown move, deferring a rule-cited finding or a task that is not open is refused with exit 1 and writes nothing.
An omitted or empty required flag, including `--reason`, is a usage error (exit 2).
A ruling never resolves or withdraws a finding and never changes a verdict.
The [interpreter's Director-ruling contract](interpreter.md#director-rulings) defines when rulings enter an attempt's immutable inputs, how they are contested, and when disputes reach the Guildmaster.

## Full review

The Director may force the task's next code review round to review the whole head, never only a rebase's resolution ([rebase-aware review](interpreter.md#rebase-aware-review)).
`task review --full` records a full-review request, receipt record `full-review/<n>`, with the mutation `full-review:<repo>:<task>:<n>`, hashed as for `task new` when longer than 128 bytes, where `n` numbers the task's requests from 1.
At most one request is pending: a retry while it is pending prints the original receipt, and once a round consumed it the next command records request `n+1`.
`--full` is required, so a later form of `task review` is not a breaking change; omitting it, `--repo` or the task, or passing `--json`, is a usage error (exit 2).
A new request for an unknown task or one that is not open is refused with exit 1 and writes nothing; replaying a pending request still returns its original receipt.
The command does not launch a review, invalidate a completed review or change an already recorded attempt binding.
The request applies to the next Artisan code review attempt whose binding has not yet been recorded, not to a human review decision or a document review.
See the [interpreter's rebase-aware review contract](interpreter.md#rebase-aware-review) for consumption, replay and scope recording.
