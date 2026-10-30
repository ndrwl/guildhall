# Decisions

A decision durably records a question for the Guildmaster and its answer (R1.61).
The record is described in [the store](store.md#records); this page is the command line for one machine's Director store.
`guildhall decide list` is the per-machine inbox of waiting decisions (R1.20); there is no push service.

## Commands

```
guildhall decide list --store <path> [--repo <id>]
guildhall decide show --store <path> --repo <id> --task <id> --decision <id>
guildhall decide answer --store <path> --repo <id> --task <id> --decision <id> --answer-file <path|->
guildhall decide defer --store <path> --repo <id> --task <id> --decision <id> --until-merged <repo>/<task>... --reason <text>
guildhall decide undefer --store <path> --repo <id> --task <id> --decision <id>
```

Every command needs an existing store and never creates one; `list` and `show` open it read-only.
Each prints one JSON document on standard output and exits 0, exits 1 when the answer or the store refuses or fails, with the reason on standard error, and exits 2 on a usage error.

- `list` prints a JSON array of the unanswered decisions, of one repository with `--repo` or of every repository, ordered by repository, task and decision id.
  Each entry is a [decision record](store.md#records); optional fields are omitted when absent, including a question's subject and any empty options.
  An empty inbox prints `[]`.
- `show` prints one decision, answered or not, including the answer, choice and note when non-empty.
- `answer` records the Guildmaster's words and prints the store receipt.
- `defer` and `undefer` are the Director's: see [deferring a decision](#deferring-a-decision).

## Answers

The answer is read from the file, or from standard input for `-`.
A leading UTF-8 byte order mark is dropped and CRLF line endings become LF, so an answer written on Windows records the same words as on Linux or macOS; UTF-16, which Windows PowerShell 5.1 writes by default, is refused with a hint to write UTF-8.
Otherwise the words are recorded exactly; a blank answer, invalid UTF-8 and more than 64 KiB are refused.

For a decision offering `approve` and `reject` under the [store's exact option-matching rules](store.md#records), this answer:

```
approve

Keep the migration behind a flag.
```

records the whole text as the answer, `approve` as the choice and `Keep the migration behind a flag.` as the note.

The answer is recorded with mutation id `decision-answer:<repo>:<task>:<decision>`, or `decision-answer:sha256:<hex>` of that text when it is longer than 128 bytes.
Repeating the same answer prints the original receipt; a different answer to an answered decision is refused, so a decision is answered once.
Answering records the answer only: it never advances, resumes or launches a task.

## Deferring a decision

`decide defer` is the Director's: it holds an open decision out of [`needs_me`](director.md#status) until every task named by `--until-merged <repo>/<task>`, which may be repeated, has merged, with the Director's `--reason` ([holds](director.md#holds), [decision](decisions/0096-hold-until-merged.md)).
The decision itself is not changed: it stays open, `decide list` still lists it, and other tasks' dependency states do not shift.
While the hold waits, `director status` lists the decision under `held` and marks it `"deferred": true` in `decisions`, and the Lanterns band does not count it; once every named task is `done`, or any is `cancelled`, it is an ordinary `decision` item again with the release appended to its reason, and the session supervisor wakes the Director once with a `hold-released` message.
Answering a deferred decision closes it as usual, and the deferral is then ignored.

Each named task must be an open task of this store other than the decision's own; one already merged is refused with `<repo>/<task> already merged; act now instead`, as is one cancelled, and an answered or unknown decision is refused too.
`defer` and `undefer` each print the deferral record as one JSON line, `{"repository":...,"task_id":...,"decision":...,"reason":...,"until_merged":[{"repository":...,"id":...}],"deferred":<bool>,"cycle":<n>}`.
As with [`step park`](running.md#step-park-and-step-unpark), the mutation id carries the record's cycle, `decision-defer:<repo>:<task>:<decision>:<cycle>`, so an identical deferral replays, one with another reason or set while deferred is refused quoting the prior one, and `undefer` keeps the record with `deferred` false and the cycle one higher; `undefer` of a decision that is not deferred is exit 1.
