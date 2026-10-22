# Messaging

A Director and its Artisans exchange short messages through the Director's store (R1.17).
`internal/messaging` and the `guildhall messages` commands implement a local, durable inbox: a message is a versioned JSON envelope that is validated and committed to the store with a receipt.
Acknowledgement removes it from the inbox, not from the store; it remains readable by id.
There is no server: each command opens the store, does one operation and exits, and nothing waits or polls in the background (R1.29).
No model is called to send, store, list or acknowledge a message, so an idle inbox spends no model tokens (R1.14).

## What exists and what does not

Supported today, on a local store file named on the command line:

- sending a message to the Director or to the Artisan of a running stage attempt;
- listing a recipient's unacknowledged messages in bounded pages;
- reading one message;
- acknowledging a consumed message;
- [waking the Director](#waking-the-director) when a message to it is stored.

Not provided yet:

- telling a running Artisan that a message arrived.
  A stored message is not proof that any agent has seen it;
- finding the store from a Director's runtime context: every command takes `--store <path>`;
- messaging another Director, another machine or anything over SSH or HTTP.
  Work still moves between Directors only by manual handoff (R1.15); [Director messaging](#director-messaging-designed-not-built) is designed, not built;
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

Decoding is strict: field names must match the wire spelling exactly, including nested artifact keys; unknown fields, duplicate keys, trailing data and envelopes over 128 KiB are refused.
Invalid UTF-8 bytes and unpaired JSON Unicode surrogate escapes are refused rather than replaced.
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
No messages command creates a store; [`store init`](store.md#guildhall-store-init) does.

- `guildhall messages send --store <path> [--config <path>] <recipient> <envelope-file|->` stores a message and prints its receipt.
  `-` reads the envelope from standard input.
  With a config, the copy's or one given with `--config`, a message about a repository the config does not list is refused by the [repositories guard rail](director.md#guard-rails).
- `guildhall messages list --store <path> [--after <sequence>] [--limit <n>] <recipient>` prints `{"messages": [...], "more": <bool>}`: at most `--limit` unacknowledged messages, 20 by default and 100 at most, in send order, with a sequence greater than `--after`.
  To read the next page, pass the last message's `sequence` as `--after`.
  Each listed message to the Director carries `wake`, its recorded [wake](#waking-the-director) (`decision`, `reason` when queued, `sender_type`, `time`), when one is recorded; a message to an Artisan has none.
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
  Omitted, null and empty artifact lists are equivalent for retries.
  Reusing an id for a different envelope is refused with the store's mutation conflict and changes nothing.
- **Nothing unread is dropped.**
  A message stays in its recipient's inbox until that recipient acknowledges it, across restarts.
  Only the recipient the message is addressed to can acknowledge it, and acknowledging records the mutation `message-ack:<id>`, so a retried acknowledgement prints the original receipt.
  Acknowledgement means the message was consumed, not that the work it mentions succeeded.
- **Reads are bounded.**
  A list returns at most 100 messages, and its work is bounded by the limit and the number of repositories, not by the size of the inbox.

The store has one writer at a time (see [the store](store.md#one-writer)).
Each command holds the store only for its own operation and waits up to two seconds for another holder, so concurrent sends are serialized rather than lost; a process that keeps the store open blocks them until it closes it.

## Waking the Director

A message to the Director is stored first and wakes second: storing never depends on waking, and a message that wakes nobody is still in the inbox.
After every send to `director`, by the engine's outbox (`task start`, `step done`, `step retry`), by an attempt's launcher sending [`worker-stalled`](artisan.md#heartbeat) or [`advance-failed`](running.md#the-watcher) or by `messages send ... director`, the sending command decides whether to wake the Director and records that decision; nothing waits in the background (R1.29).
An advance records its sends under the task lock and wakes for them, one wake per message in send order, after releasing the lock, whether or not the advance succeeded, so the lock is held for records and launches only; the others wake straight after the send.
Each wake is bounded by ten seconds, and each `herdr` call within it ends with its context ([herdr](contracts/herdr.md)), so a slow or hung pane holds neither the task nor the sender past that bound; a wake past it is recorded `queued` with the pane's error.

The command reads the Director's newest [session](store.md#director-sessions).
When it is `running` or `recovering` and has a bound pane, the command types one line and Enter into that pane, which Guildhall created for the Director's session (R1.54), and records `woke`:

```text
guildhall: 3 unread (attempt-failed 1, question 2)
```

The line counts the Director's unread messages by type, in type order, and never carries message text, so no sender can type instructions into the Director's session.
Because a sender chooses its message's type, the line names only the types in `store.MessageTypes`, the one registry of the types Guildhall sends, each with its sender: those the engine and the launcher send (`advance-failed`, `attempt-failed`, `attempt-retried`, `budget-refused`, `changes-requested`, `checks-failed`, `checks-passed`, `checks-pending`, `decision`, `director-stage`, `loop-exit-rejected`, `merge-held`, `merge-refused`, `merged`, `published`, `stall`, `worker-stalled`, `workspace-kept`) and the local `question` and `note`; every other type counts as `other`, and `DirectorUnread` counts by the same registry.
The engine sends only by the registry's constants, which a test of `internal/interp` checks at every send, so a type is added to the registry by the change that sends it and never by a sender.
When an [automatic handoff](director.md#automatic-handoff) trigger holds, the line ends `; handoff due (<reason>)`, such as `; handoff due (context 72%)`, and the wake records the crossing, so the reminder rides on a line the Director reads anyway; the reason is Guildhall's own text, never a sender's.
Otherwise nothing is typed and the wake is recorded `queued` with its reason, and the message waits for the Director's next turn:

| Reason | When |
|---|---|
| `no session` | No Director session has been recorded. |
| `paused`, `stopping`, `stopped` | The newest session is in that state; a paused Director is not woken. |
| `no pane` | The session has no bound pane: it runs in a terminal Guildhall did not create. |
| `nothing unread` | The Director has no unread message: this one was acknowledged before the wake counted unread messages. |
| `runtime not running` | The pane's shell is idle, so the Director's runtime has ended and the line would reach a shell prompt. |
| `pane: <error>` | Herdr refused or failed the typing, for example a closed tab. |

A message is woken for at most once: the wake records the mutation `wake:<id>`, and a retried send finds the recorded wake and types nothing new.
The line is typed before the decision is recorded, so a send retried after a crash between the two may type it again.
A failed wake never fails the send; the command reports the wake's failure on standard error, and `messages send` still prints the receipt as usual.
The pane is reached through the Herdr session it was recorded in, with `herdr` found on `PATH`.

The separate [resume wake](director.md#pause-and-resume) leaves each message's recorded wake unchanged.

Every wake records its sender type: `guildhall` for the engine and `local` for `messages send`, which cannot tell the Guildmaster from an Artisan.
Both wake on every message; the per-sender [wake policies](#waking-a-director) below are designed, not built.

## Director messaging (designed, not built)

Everything in this section is designed, not built, except the [local wake](#waking-the-director) and its wake record.
No command, flag, config key or other store record below exists yet; later slices build each contract red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [Director messaging](decisions/0049-director-messaging.md).
It extends the local inbox above so a Director can hear from the Guildmaster and from other Directors, without giving any message more authority than the local inbox has today.

### Who may send

A Director's inbox has two external sender types:

| Sender type | Who | Default wake |
|---|---|---|
| `guildmaster` | The Guildmaster, by their own command | Always wakes |
| `director` | Another Director, on this machine or another | Draws on the [wake budget](#waking-a-director) |

The Steward never messages a Director.
It changes a Director by command: it copies `managed.toml` as rollouts do today and may then run `guildhall config reload` on the copy (see [Standalone and managed Directors](director-modes.md#how-the-steward-acts)).
The Director's binary records each managed-settings revision it applies as a store event, which the task timeline and the status feed show; nothing about it enters the inbox.

Messages from the Director's own Artisans keep the existing `artisan:` and `director` envelope and are unchanged: they are internal to one Director, never cross a machine and are not a sender type above.
Waking the Director for an Artisan message is out of scope here.

### Authority

The [existing rule](#authority) holds without exception: a message informs only.
It never records a result, answers a decision, approves a plan or a merge, or starts, stops or resumes anything.

A message from another Director may at most become a ticket in the receiving Director's queue:

- the receiving Director agent reads the message and judges whether it is worth a ticket;
- filing a ticket records a [ticket](#store-records) and opens a Guildmaster decision on whether the work is worth doing;
- a ticket is not a task: it has no workflow revision, starts no step and launches no Artisan;
- only the Guildmaster's answer `accept` lets the Director create the task with `guildhall task new`, as for any other task; `decline` closes the ticket.

So no Director can cause work in another Director's repositories without the Guildmaster's explicit answer.
Shared lessons never travel Director to Director: a managed Director proposes them to the Steward (R1.32) and a standalone one keeps them locally (see [lessons](director-modes.md#lessons)).

### Message types

Director messages are typed, not free chat.
The type decides which fields are required and whether the message may be filed as a ticket.

| Type | Meaning | May become a ticket |
|---|---|---|
| `handoff-offer` | The sender offers work, with a handoff bundle reference, for the receiver to take on | Yes |
| `dependency-notice` | The sender's work depends on, or changes something used by, the receiver's repositories | Yes |
| `question` | The sender asks something it cannot find out itself | No |
| `answer` | A reply to one `question`, naming it in `in_reply_to` | No |

A Guildmaster message has the single type `note` and is never a ticket: the Guildmaster tells a Director what to do directly (R1.6).
The first real use is the manual handoff (R1.15): `handoff-offer` tells the receiver a handoff bundle is ready for `guildhall handoff import`, which the Guildmaster still runs or approves.
Further types are added by a decision, never by a sender.

### Envelope

A Director message is a new schema, `guildhall.director-message/1`, so the existing `guildhall.message/1` stays exactly as it is.
It names no task of the receiver's store, because the receiver may have none for it yet.

```json
{
  "schema": "guildhall.director-message/1",
  "id": "web-msg-7",
  "from": "web",
  "to": "tools",
  "type": "dependency-notice",
  "subject": "web now needs the cli --json flag on release 3",
  "text": "Task web/t-12 parses `cli list --json`. It lands after cli ships the flag.",
  "about": {"repository": "web", "task": "t-12"},
  "artifacts": [
    {"path": "notes/cli-json.md", "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
  ]
}
```

| Field | Contract |
|---|---|
| `schema` | `guildhall.director-message/1`; a newer version is refused (R1.11). |
| `id` | Chosen by the sender, unique among the sender's messages; the same id rules as `guildhall.message/1`. |
| `from` | The sending Director's name. The receiver checks it against the transport identity and refuses a mismatch; see [identity](#identity). |
| `to` | The receiving Director's name; it must match the receiving copy. |
| `type` | One of the [message types](#message-types). |
| `subject` | The one-line header the receiver sees before reading: non-blank valid UTF-8, no line break, at most 200 bytes. |
| `text` | As in `guildhall.message/1`: non-blank, at most 16 KiB. |
| `about` | Optional: the sender's repository and task the message concerns, which the receiver does not look up. |
| `in_reply_to` | Required for `answer` and refused otherwise: the id of the `question` it answers, in the receiver's own outbox. |
| `artifacts` | As in `guildhall.message/1`; the files stay with the sender, and a `handoff-offer` names its handoff bundle here. |

Decoding follows the same strict rules as `guildhall.message/1`, including the 128 KiB envelope limit, encodings and duplicate keys.
A Guildmaster `note` uses the same schema with `from` set to `guildmaster`, no `about` and no `in_reply_to`.

### Waking a Director

A message is stored first and wakes second: storing never depends on waking, and a message that wakes nobody is still in the inbox.
There is no service (R1.29): the `receive` command that stores a message decides then and there whether to wake, and records that decision.
Waking is the [local wake](#waking-the-director), which is built: one line of counts typed into the pane Guildhall created for the Director's session, with no bound pane queueing the message for the Director's next turn.

Each sender type has its own wake policy in config:

- `always`: every message wakes; the Guildmaster's default, and the only policy allowed for `guildmaster`.
- `budget`: the Director's default for `director` messages.
  At most `wakes` message-driven wakes are allowed in each `window`.
  When a window ends with its budget spent, the next window is twice as long, up to `backoff_max`; a window that ends with budget to spare resets to `window`.
  While the budget is spent, messages keep queueing and no wake happens; the first message after the budget renews wakes, so no timer is needed.
- `never`: messages queue and are seen on the Director's next turn.

There is no per-sender limit: one Director sending many messages and many Directors sending one each draw on the same budget.

On every wake, and at the start of every Director turn, the Director first sees the mailbox summary: the unread count per sender type, whether the director inbox is open, and one header per unread message (sender, message id, type, subject, sequence), newest last, at most 20.
It then chooses to read, skim or skip; reading a message never acknowledges it, and acknowledging means the Director has dealt with it.

### Spam and a full mailbox

When the Director agent judges that other Directors' messages are spam, it may close its director inbox with a reason.
Closing raises a system alert: a `question` decision with options `reopen` and `keep-closed`, which `guildhall decide list` shows the Guildmaster.
Only the Guildmaster reopens the inbox: `guildhall messages inbox open` is refused unless the alert's recorded answer is `reopen`, so the Director cannot reopen it on its own.
While closed, a new `director` message is refused at `receive` and the sender's outbox records the refusal; a `guildmaster` message is never refused by closing.

`mailbox_max` caps unacknowledged `director` messages.
At the cap, `receive` refuses a new message with `mailbox full`, stores nothing and tells the sender; a new delivery attempt after the receiver acknowledges messages can be accepted.
Identical committed retries return their original receipt before either admission check, even when the inbox is now closed or full; conflicting envelopes are still refused.
The Guildmaster's messages are never refused for a full mailbox.

### Transport between machines

Two options were considered.

1. **Relay through the Steward.**
   The Steward already reaches every enrolled Director over SSH, so a Director would hand its message to the Steward and the Steward would copy it on.
   It makes messaging need a Steward, which [standalone Directors](director-modes.md) forbid, puts the Steward in every conversation, and gives the Steward a second job besides rollouts that acts on request (R1.7).
2. **Director to Director over SSH (recommended).**
   The sender runs `ssh -o BatchMode=yes <alias> guildhall messages receive --from <sender> --to <receiver> -` with a fixed argument list and the envelope on standard input, the same shape as Steward rollouts' fixed `scp` calls.
   The receiving account restricts the sender's key to that command with an `authorized_keys` forced command, so the key can do nothing else and its `--from` is fixed by the key, not by the sender.
   Two Directors on the same machine and account run `guildhall messages receive` locally against the receiver's copy, with no SSH.

Option 2 needs SSH from one Director machine to another, which R1.8 does not set up today; setting it up is a manual enrolment step shown as exact commands (R1.47), never done by Guildhall.
A Director finds its peers in `[[peer]]` config entries written by hand in its own local config, in both modes: managed settings are flat string keys and cannot carry a table ([Director modes](director-modes.md#what-is-missing)), so peers stay local until managed tables exist.

### Identity

- Over SSH, the sender is the identity bound to its key by the forced command; the envelope's `from` must equal it.
- Locally, on one account, the sender is asserted, not authenticated, exactly as for the local inbox today: anything that can write the store can send.
  Directors sharing an account are one trust domain.
- The Guildmaster sends with `guildhall messages send --as guildmaster` locally or through a key whose forced command fixes `--from guildmaster`.
  A local `--as guildmaster` is likewise asserted, so the `always` wake policy is no stronger than the account it runs on.

### Delivery guarantees

Delivery between Directors is at least once, made exactly once in the receiver's store by message ids and idempotent receipts, as the local inbox already does.

- The sender first records the message in its own store's outbox as `pending`, with mutation `outbox:<id>`, then calls `receive`.
- At the shared local/SSH receive boundary, after envelope and transport-identity validation, the receiver resolves mutation `dmessage:<from>:<id>` before checking closure or capacity.
  An identical committed retry returns the original receipt, stores nothing new and never wakes again, including after acknowledgement, closure or reaching capacity.
  Reusing that identity for a different envelope is refused with the store's mutation conflict.
  Only a new identity proceeds through admission, storage and the wake decision, serialized in the receiving store so concurrent retries cannot duplicate admission or waking.
- The receiver's receipt, printed on standard output, is recorded in the sender's outbox as `delivered`; a refusal (`closed`, `mailbox full`, invalid) is recorded as `refused` with its reason and is not retried.
- A lost result or a failed connection leaves the outbox entry `pending`.
  `guildhall messages outbox --retry` retries pending entries; nothing retries in the background.
- Delivered means stored in the receiver's inbox; it does not mean read, and never means accepted as work.

### Commands

The existing `guildhall messages send|list|read|ack` keep their meaning for the local inbox.
External messages use the full `(sender, message id)` identity in every lookup and reference.
The `--sender <sender>` flag selects the exact external sender (including `guildmaster`), not the sender-type filter `--from`; omitting it on read or ack selects only the existing local inbox and never guesses an external sender.
External list, read and summary output include both identity fields.
External acknowledgements use mutation `dmessage-ack:<sender>:<id>` and return the original receipt on retry.
The external API likewise takes both fields for read and ack; the existing local `Mailbox` API remains unchanged.

The designed additions are:

| Command | Does |
|---|---|
| `guildhall messages send --to <director> <envelope-file\|->` | A new form beside the existing positional-recipient `send`: records a Director message in the sender's outbox and delivers it to the named peer. `--as guildmaster` sends a Guildmaster `note`. |
| `guildhall messages receive --from <sender> --to <director> <envelope-file\|->` | The receiving side: validates, resolves committed retries or conflicts first, then applies the closed and `mailbox_max` rules only to new messages, stores, decides and records the wake, and prints the receipt or the refusal. Run by `send` locally or over SSH, never by hand. |
| `guildhall messages summary` | Prints the mailbox summary a wake shows: counts, inbox state and headers. |
| `guildhall messages list --from director\|guildmaster` | Lists external messages filtered by sender type, including sender and message id. |
| `guildhall messages read --sender <sender> <message-id>` | Reads the exact external message, acknowledged or not, without acknowledging it. |
| `guildhall messages ack --sender <sender> director <message-id>` | Acknowledges the exact external message addressed to this Director and prints its receipt. |
| `guildhall messages outbox [--retry]` | Lists the sender's outbox entries and their states; `--retry` retries pending ones. |
| `guildhall messages inbox close --reason <text>` | Closes the director inbox and raises the system alert. |
| `guildhall messages inbox open` | Reopens it; refused unless the Guildmaster answered the alert `reopen`. |
| `guildhall ticket file --sender <sender> --from-message <id>` | Requires both identity fields; files a ticket from that sender's `handoff-offer` or `dependency-notice` and opens its Guildmaster decision. |
| `guildhall ticket list` | Lists tickets and their states. |

Every command keeps the existing conventions: one JSON document on success, exit 1 on refusal or failure, exit 2 on a usage error, no messages command creates a store ([`store init`](store.md#guildhall-store-init) does).

### Config keys

Wake policy and mailbox settings are ordinary [settings](configuration.md#precedence), so a managed copy's Steward may manage them and a standalone copy sets them in `[local]`:

| Key | Default | Meaning |
|---|---|---|
| `wake_guildmaster` | `always` | `always` only; any other value is refused. |
| `wake_director` | `budget` | `budget` or `never`. |
| `wake_director_wakes` | `4` | Wakes allowed per window. |
| `wake_director_window` | `1h` | Base window, a Go duration of at least `1m`. |
| `wake_director_backoff_max` | `24h` | Longest window after repeated spent budgets. |
| `mailbox_max` | `200` | Unacknowledged `director` messages before `receive` refuses. |

Peers are a new config table, local in a standalone copy:

```toml
[[peer]]
name = "web"
machine = "laptop"
ssh_alias = "guild-laptop"
copy_dir = "/home/guild/directors/web"
```

`ssh_alias` and `copy_dir` follow the roster's character rules; a peer on this machine and account omits `ssh_alias`.

### Store records

All are new records in the receiver's or sender's existing store, keyed outside any repository, so the per-repository buckets are unchanged.

| Record | Key | Contract |
|---|---|---|
| Director message | sender, message id | The envelope, the sender type, the sequence, and an inbox entry until acknowledged once; never changes another record. |
| Outbox entry | message id | The envelope sent, the peer, and a state `pending`, `delivered` with the receiver's receipt, or `refused` with its reason. |
| Wake | sequence | Built for the local wake: each decision's message id, sender type, `woke` or `queued` with the reason, and time ([store](store.md#records)). The budget adds the reasons `budget spent` and `never` and computes each window from the wakes' sender types and times, so it survives restarts with no change to the record. |
| Inbox state | `director` | `open` or `closed`, with the reason, the alert decision and who changed it. |
| Ticket | repository, ticket id | The source message's full `(sender, message id)` reference, the proposed title and repository, its decision, and a state `proposed`, `accepted` or `declined`, changed once. `accepted` records the task id later created. |

A ticket's decision is a decision of kind `question` with options `accept` and `decline`, and the inbox alert one with options `reopen` and `keep-closed`.
Decisions are keyed by repository and task today, so both need the decision record's key to admit a ticket or the inbox as its subject; that is the first contract the ticket slice changes.

### Conflicts to resolve

These are flagged rather than chosen silently:

- **R1.15** says work moves between Directors only by manual handoff with no automatic cross-Director scheduling; a ticket schedules nothing and needs the Guildmaster's answer, but the requirement does not yet mention Director messages.
- **R1.20** says there is no push service; a wake is a push into a pane by the `receive` command, with no service.
- **R1.8** sets up SSH from the Steward to Directors only; Director-to-Director transport needs SSH between Director machines.
- **D15** deferred messaging another Director, transports and notifications; this design is that follow-up and extends D15 rather than replacing it.
- **D12** says no remote command is run; `messages receive` over SSH is a Director's remote command, not a rollout's, and does not change rollouts.
