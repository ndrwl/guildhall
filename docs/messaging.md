# Messaging

A Director and its Artisans exchange short messages through the Director's store (R1.17).
`internal/messaging` and the `guildhall messages` commands implement a local, durable inbox: a message is a versioned JSON envelope that is validated, committed to the store with a receipt and kept until its recipient acknowledges it.
There is no server: each command opens the store, does one operation and exits, and nothing waits or polls in the background (R1.29).
No model is called to send, store, list or acknowledge a message, so an idle inbox spends no model tokens (R1.14).

## What exists and what does not

Supported today, on a local store file named on the command line:

- sending a message to the Director or to the Artisan of a running stage attempt;
- listing a recipient's unacknowledged messages in bounded pages;
- reading one message;
- acknowledging a consumed message.

Not provided yet:

- telling a running agent that a message arrived.
  A stored message is not proof that any agent has seen it;
- finding the store from a Director's runtime context: every command takes `--store <path>`;
- messaging another Director, another machine or anything over SSH or HTTP.
  Work still moves between Directors only by manual handoff (R1.15);
- authentication of the sender: anyone who can write the store file can send a message.

## Authority

A message carries information only.
Sending, reading or acknowledging a message never records a task or attempt result, answers a decision, approves a plan or a merge, or starts, stops or resumes anything.
An Artisan's result still comes from its validated result file (R1.23), Guildmaster answers still go to decisions (R1.61) and merges still follow the repository's merge policy (R1.36), whatever a message says.
The message `type` is a label for the reader; Guildhall does not interpret it.

## Recipients

There are two recipient forms:

| Recipient | Inbox |
|---|---|
| `director` | The one Director that owns the store. It spans every repository in the store. |
| `artisan:<repository>/<task>/<step>/<attempt>` | The Artisan running that stage attempt, for example `artisan:app/t-1/build/1`. |

There is no Director or Artisan registry yet, so an Artisan is identified by the stage attempt it runs.
The repository, task, step and attempt must already exist in the store; a message to an Artisan needs that attempt to be running, while a message to the Director may concern a finished attempt or only a task.
No inbox is created for any other name.
Recipients are store keys, never file paths.

## Envelope

A message body is one JSON object, read from a file or from standard input, never from a command-line argument, so no shell quoting is involved.

```json
{
  "schema": "guildhall.message/1",
  "id": "t-1-build-1-q1",
  "recipient": "director",
  "repository": "app",
  "task": "t-1",
  "step": "build",
  "attempt": 1,
  "type": "question",
  "text": "The plan names two base branches. Which should the fix target?",
  "artifacts": [
    {"path": "tasks/t-1/branches.md", "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
  ]
}
```

| Field | Contract |
|---|---|
| `schema` | `guildhall.message/1`. A newer version is refused rather than guessed at (R1.11). |
| `id` | Chosen by the sender and unique in the store: 1 to 100 letters, digits, `.`, `_` and `-`, starting with a letter or digit. |
| `recipient` | `director` or `artisan`; it must match the recipient named on the command line. |
| `repository`, `task` | The task the message concerns, which must exist. |
| `step`, `attempt` | The stage attempt the message concerns, given together. Required for an Artisan recipient, whose inbox they name; optional for the Director. |
| `type` | A lowercase id such as `question`, `progress` or `note`. |
| `text` | Concise prose: non-blank valid UTF-8 of at most 16 KiB. |
| `artifacts` | Optional, at most 32: files the message refers to, each a clean relative slash-separated `path` and the lowercase hex `sha256` of its content. The files themselves are not stored. |

Decoding is strict: unknown fields, a field given twice (including in a different letter case), trailing data and envelopes over 128 KiB are refused.
A leading UTF-8 byte order mark and CRLF line endings, as some Windows tools write, are accepted.
UTF-16, which Windows PowerShell 5.1 writes for `>` and `Out-File`, is refused with a hint rather than misread; write the file as UTF-8, for example with `Set-Content -Encoding utf8`, or pipe it to `-` from PowerShell 7 or Bash.

## Go API

`messaging.Mailbox` is the contract consumers use: `Send`, `List`, `Read` and `Ack` with the guarantees below.
`messaging.StoreMailbox` implements it on a store and is what the commands use.
`messagingtest.RunMailboxContract` is the behavioural suite every implementation must pass, and `messagingtest.Fake` is an in-memory implementation that passes it, for testing a consumer without a store file.
The fake is not durable and not shared between processes.

## Commands

Flags come before the positional arguments.
Each command prints one JSON document on success and exits 0; it exits 1 when the store is missing, locked, refuses the request or fails, and 2 on a usage error, printing the reason on standard error.
No command creates a store.

- `guildhall messages send --store <path> <recipient> <envelope-file|->` stores a message and prints its receipt.
  `-` reads the envelope from standard input.
- `guildhall messages list --store <path> [--after <sequence>] [--limit <n>] <recipient>` prints `{"messages": [...], "more": <bool>}`: at most `--limit` unacknowledged messages, 20 by default and 100 at most, in send order, with a sequence greater than `--after`.
  To read the next page, pass the last message's `sequence` as `--after`.
- `guildhall messages read --store <path> <message-id>` prints one message, acknowledged or not, without acknowledging it.
- `guildhall messages ack --store <path> <recipient> <message-id>` acknowledges a message addressed to that recipient and prints the acknowledgement's receipt.

A listed or read message is its envelope plus `sequence`, its store-wide order, and `acknowledged`.

## Delivery guarantees

- **Accepted means stored.**
  `send` prints a receipt only after the message, its inbox entry and its receipt are committed in one store transaction, so after a crash either all are present or none is.
  It does not mean the recipient has read it.
- **Retries are safe.**
  A send records the mutation `message:<id>`.
  Sending the same envelope again, for example after losing the first result, prints the original receipt and stores nothing new, even after a restart or after the message was acknowledged.
  Reusing an id for a different envelope is refused with the store's mutation conflict and changes nothing.
- **Nothing unread is dropped.**
  A message stays in its recipient's inbox until that recipient acknowledges it, across restarts.
  Only the recipient the message is addressed to can acknowledge it, and acknowledging records the mutation `message-ack:<id>`, so a retried acknowledgement prints the original receipt.
  Acknowledgement means the message was consumed, not that the work it mentions succeeded.
- **Reads are bounded.**
  A list returns at most 100 messages, and its work is bounded by the limit and the number of repositories, not by the size of the inbox.

The store has one writer at a time (see [the store](store.md#one-writer)).
Each command holds the store only for its own operation and waits up to two seconds for another holder, so concurrent sends are serialized rather than lost; a process that keeps the store open blocks them until it closes it.
