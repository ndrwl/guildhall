# Tasks

A task is one unit of work in one repository a Director owns.
The `guildhall task` commands create tasks in the Director's store and read them back.
Creating a task records it and binds the exact workflow and configuration it was accepted under (R1.62); it launches nothing, calls no model and starts no step.
For decisions and their answers, see the [decision commands](decisions-cli.md).

## Commands

Every command takes `--store <path>`, an existing store file; no command creates a store.

- `guildhall task new --store <path> --config <director.toml> --repo <id> --title <text> [--depends-on <repo>/<task>]... <task>` creates task `<task>` in repository `<id>` and prints the store's receipt as one JSON document.
- `guildhall task show --store <path> --repo <id> <task> --json` prints `{"task": ..., "attempts": [...], "documents": [...], "decisions": [...]}` for one task.
- `guildhall task list --store <path> [--repo <id>] --json` prints a JSON array of tasks, ordered by repository and then task id, for one repository or every repository in the store.
- `guildhall task timeline --store <path> --repo <id> <task> --json` and `guildhall task files --store <path> --repo <id> <task> --json` are the [inspection reads](#inspection).

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
A missing or unreadable file, a parse error, a refused override or a validation problem refuses the task with the problems named, and the store is not opened, so nothing is written.

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
- `sequence` and `committed_at`: the newest mutation committed anywhere in the store when the read began. Two reads with the same sequence saw the same records.
- `workflow`: the workflow revision (`name`, `digest`) the task is bound to.

Times are commit times of the store's [receipts](store.md#mutations-and-receipts), in UTC; they record when Guildhall wrote a record, not when a worker did the work.
An unknown repository or task fails with exit 1 and the store's not-found error.

`task timeline` prints:

- `omitted_kinds`: `findings` and `director_rulings`, each labelled `not yet recorded by this version`.
- `task`: the task record.
- `attempts`: every stage attempt with its outcome (absent while running), `started_at`, `finished_at` and `duration_seconds` between them, ordered by when they started.
- `decisions`: every decision with its kind, subject, options, question and the Guildmaster's exact words (`answer`, `choice`, `note`), plus `opened_at`, `answered_at` and `wait_seconds` between them, ordered by when they opened.
- `events`: every receipt of the task in commit order, each naming its operation, mutation id, sequence, commit time and the record it wrote (`record`, for example `attempt/build/2`, `decision/accept-plan` or `message/q-1`). These are the moves made on the task, including messages sent and acknowledged for it.

`task files` prints `documents`: every document reference of the task with its relative path, its recorded `sha256` and `recorded_at`, in name order.
The digest is the one recorded when the reference was written; `files` does not open or re-hash the document.

Receipts committed before receipts named their record have no `record`; they still appear in `events`, but cannot supply record timestamps. Timestamps are matched by operation: an attempt finished or a decision answered after an upgrade can have a known completion time with an unknown start. Durations require both endpoints; attempts and decisions without a start time follow the timed ones in store order.
The timeline includes every recorded event kind in commit order. Review findings and Director rulings are not yet recorded by this version, as explicitly listed in `omitted_kinds`; their persistence belongs to the interpreter slices.
Reading a task's diff, process status or transcript needs its worktree and adapter, and is separate work.

