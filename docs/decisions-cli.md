# Decisions

A decision is a durable pause: a task waits on it, with no model calls, until the Guildmaster answers (R1.61).
The record is described in [the store](store.md#records); this page is the command line for one machine's Director store.
`guildhall decide list` is the per-machine inbox of waiting decisions (R1.20); there is no push service.

## Commands

```
guildhall decide list --store <path> [--repo <id>]
guildhall decide show --store <path> --repo <id> --task <id> --decision <id>
guildhall decide answer --store <path> --repo <id> --task <id> --decision <id> --answer-file <path|->
```

Every command needs an existing store and never creates one; `list` and `show` open it read-only.
Each prints one JSON document on standard output and exits 0, exits 1 when the answer or the store refuses or fails, with the reason on standard error, and exits 2 on a usage error.

- `list` prints a JSON array of the unanswered decisions, of one repository with `--repo` or of every repository, ordered by repository, task and decision id.
  Each entry has the repository, task and decision ids, the kind, the subject's kind and digest, the options and the question; decisions recorded before typed decisions have no kind, subject or options.
  An empty inbox prints `[]`.
- `show` prints one decision, answered or not, including the recorded answer, choice and note.
- `answer` records the Guildmaster's words and prints the store receipt.

## Answers

The answer is read from the file, or from standard input for `-`.
A leading UTF-8 byte order mark is dropped and CRLF line endings become LF, so an answer written on Windows records the same words as on Linux or macOS; UTF-16, which Windows PowerShell 5.1 writes by default, is refused with a hint to write UTF-8.
Otherwise the words are recorded exactly; a blank answer, invalid UTF-8 and more than 64 KiB are refused.

When the decision offers options, the first line of the answer, with surrounding white space removed, must equal one of them exactly, case included:

```
approve

Keep the migration behind a flag.
```

records the whole text as the answer, `approve` as the choice and `Keep the migration behind a flag.` as the note.
An answer whose first line names no option is refused and nothing is written.
A decision without options takes any non-blank answer.

The answer is recorded with mutation id `decision-answer:<repo>:<task>:<decision>`, or `decision-answer:sha256:<hex>` of that text when it is longer than 128 bytes.
Repeating the same answer prints the original receipt; a different answer to an answered decision is refused, so a decision is answered once.
Answering records the answer only: it never advances, resumes or launches a task.
