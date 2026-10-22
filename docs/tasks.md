# Tasks

A task is one unit of work in one repository a Director owns.
The `guildhall task` commands create tasks in the Director's store and read them back.
Creating a task records it and binds the exact workflow and configuration it was accepted under (R1.62); it launches nothing, calls no model and starts no step.
For decisions and their answers, see the [decision commands](decisions-cli.md); `task start`, which advances a task, is in [running a task](running.md).

## Commands

Every command takes `--store <path>`, an existing store file made by [`guildhall store init`](store.md#guildhall-store-init), or a [copy](running.md#copies) with `--copy <dir>` or `GUILDHALL_COPY`, whose `director.store` is the store and whose `config.toml` is `task new`'s `--config`.

- `guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--depends-on <repo>/<task>]... <task>` creates task `<task>` in repository `<id>` and prints the store's receipt as one JSON document.
- `guildhall task show --store <path> --repo <id> <task> --json` prints `{"task": ..., "attempts": [...], "documents": [...], "decisions": [...]}` for one task.
- `guildhall task list --store <path> [--repo <id>] --json` prints a JSON array of tasks, ordered by repository and then task id, for one repository or every repository in the store.
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
The effective workflow's profile references are then checked with `config.CheckProfiles`; every named profile must be declared in the loaded Director configuration.
A missing or unreadable file, a parse error, a refused override, a validation problem or an undeclared profile refuses the task with the problems named, and the store is not opened, so nothing is written.

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
