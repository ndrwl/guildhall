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
- [waking the Director](#waking-the-director) when a message to it is stored;
- [Steward messages](#steward-messages): the Steward telling an enrolled Director about an issue it found, which the Director's external inbox receives from the `steward` sender;
- [steering a running attempt](#steering-an-attempt): every `guildhall` command the Artisan runs shows its unacknowledged messages, and intake refuses a succeeded result until each is acknowledged.

Not provided yet:

- showing a running Artisan a message between its `guildhall` commands on a runtime with no hook: only Claude has the [after-tool-use hook](#steering-an-attempt).
  An Artisan elsewhere that runs no `guildhall` command for a long stretch sees a message at its next one, or when its result is refused;
- finding the store from a Director's runtime context: every Director command takes `--store <path>` or a copy;
- messaging another Director, on this machine or another.
  Work still moves between Directors only by manual handoff (R1.15); [Director messaging](#director-messaging-designed-not-built) is designed, not built;
- authentication of the sender: anyone who can write the store file can send a message.

## Authority

A message carries information only.
Sending, reading or acknowledging a message never records a task or attempt result, answers a decision, approves a plan or a merge, or starts, stops or resumes anything.
An Artisan's result still comes from its validated result file (R1.23), Guildmaster answers still go to decisions (R1.61) and merges still follow the repository's merge requirements (R1.36), whatever a message says.
The message `type` is a label for the reader; Guildhall does not interpret it.

## Recipients

There are three recipient forms:

| Recipient | Inbox |
|---|---|
| `director` | The one Director that owns the store. It spans every repository in the store. |
| `steward` | The [Steward session](steward.md#inbox) that owns a Steward copy's `steward.store`, and only there. It spans every repository in the store, and takes messages from the `guildmaster` sender and, from its own scheduled collection only, the `director` tag. |
| `artisan:<repository>/<task>/<step>/<attempt>` | The Artisan running that stage attempt, for example `artisan:app/t-1/build/1`. |

`messages send` refuses `director` to a `steward.store` and `steward` to any other store, and refuses a `steward` message from any sender but `guildmaster`, so no Director, other Director or Artisan reaches the Steward, and the Steward session's own sends are `director` sends that `messages send` refuses.
The store takes a `steward` message from `guildmaster` and `director` only (`store.StewardSenders`); the `director` tag is the Steward supervisor's own, for the `triage-opened` report of its [scheduled collection](steward.md#scheduled-collection) about items Directors filed.
A message to `steward` is woken for as a message to `director` is, and both are the store's session inbox.
None of this changes for [Steward messages](#steward-messages), which go the other way, from the Steward to a Director's external inbox.

An `artisan:` inbox takes messages from the `director` and `guildhall` senders only (`store.ArtisanSenders`): `messages send` to it must run in the session of the Director whose copy runs the attempt, and the store refuses any other sender, so the Guildmaster steers an attempt through the Director, and no Artisan, Steward or other Director reaches one.

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

Inside an attempt, where `GUILDHALL_ATTEMPT` is set, only the Artisan forms exist, on the attempt's own inbox in the store of the copy that runs it, found from the working directory as `guildhall result` finds it:

- `guildhall messages [list] [--after <sequence>] [--limit <n>]` lists the attempt's unacknowledged messages, printing what `messages list --store <path> artisan:<attempt>` prints.
- `guildhall messages ack <message-id>` acknowledges one of them and prints the receipt; acknowledging it again prints the same receipt.

They refuse `--store`, `--copy`, `--config`, a recipient, `read` and `send` with exit 2, so an Artisan reads and acknowledges only its own messages and sends none.

`guildhall messages hook` is the Claude Artisan's [after-tool-use hook](#steering-an-attempt): it prints a Claude Code hook document only when the attempt has unacknowledged messages, and always exits 0, inside an attempt or not.

A listed or read message is its envelope plus `sender`, `sequence`, its store-wide order, and `acknowledged`:

| Field | Contract |
|---|---|
| `sender` | Where the message was sent from, set by Guildhall, never by the envelope: one of the [senders](#senders) below. Omitted for a message stored before senders were recorded, which reads as `unknown`. |
| `sequence` | The send receipt's sequence, the message's store-wide order. |
| `acknowledged` | Whether the recipient has acknowledged the message. |

### Senders

Guildhall records each message's `sender` from where it was sent, from a fixed vocabulary, so no free text ever reaches the field.
An envelope cannot declare its own sender: `sender` is not an envelope field, so strict decoding refuses an envelope that carries one.

| Sender | Set when |
|---|---|
| `guildhall` | Guildhall sends in process: the engine's outbox, an attempt's launcher, a quality judge and the workspace close sweep. |
| `artisan` | A command runs with `GUILDHALL_ATTEMPT` set, which the launcher sets to `<repository>/<task>/<step>/<n>` for an attempt's launcher and worker. This takes precedence over every row below, since a worker started from a Director session inherits its session variables. `messages send` is refused there, so no new message records it; older messages may carry it. |
| `director` | `GUILDHALL_SESSION` is set and [copy discovery](running.md#copies) from where `messages send` runs finds the copy whose store the message is sent to. A Director session runs inside its copy; `--copy` does not choose the sender's copy. A Steward session sending to its own `steward.store` is `director` too. In a Steward store it is also the tag of the scheduled collection's `triage-opened` report, which the Steward's supervisor sends in process. |
| `other-director` | `GUILDHALL_SESSION` is set and copy discovery finds a different copy. |
| `guildmaster` | Neither `GUILDHALL_SESSION` nor `GUILDHALL_ATTEMPT` is set, wherever it runs. |
| `steward` | `messages receive` took in a [Steward message](#steward-messages). It is the sender of an external message only, never of `messages send`, whose sender comes from its environment, so no local process becomes the Steward by setting a variable. |
| `unknown` | Anything else, such as `GUILDHALL_SESSION` where copy discovery finds no copy. A message stored with no sender also reads as `unknown`. |

The copy's name is not stored.
A sender is asserted by the sending process's environment, not authenticated: anything that can write the store, or set those variables, can claim any sender but `guildhall`.

## Delivery guarantees

- **Accepted means stored.**
  `send` prints a receipt only after the message, its inbox entry and its receipt are committed in one store transaction, so after a crash either all are present or none is.
  It does not mean the recipient has read it.
- **Retries are safe.**
  A send records the mutation `message:<id>`.
  Sending the same envelope again, for example after losing the first result, prints the original receipt and stores nothing new, even after a restart or after the message was acknowledged.
  The sender is not part of the request, so the same envelope resent from another place, or replayed by the engine's outbox after an upgrade, returns the original receipt and keeps the first sender.
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

## Steering an attempt

The Director steers a running stage attempt by sending a message to its `artisan:<repository>/<task>/<step>/<attempt>` inbox, for example "the base moved, rebase first".
The message carries text only, as every message does: stopping an attempt stays with `artisan stop`, and changing its brief with send-back or retry.
Delivery does not depend on the agent, a hook or a plugin; it rides on the `guildhall` commands every Artisan already runs, and intake enforces it.

- **Shown on every command.**
  `guildhall ci log`, `guildhall result check`, `guildhall result submit` and `guildhall messages`, run with `GUILDHALL_ATTEMPT` set, first print each unacknowledged message to the attempt on standard error, in a frame:

  ```text
  ==== guildhall: steering message steer-1 from director (note) ====
  | The base moved; rebase onto main first.
  ==== act on it, then acknowledge it: guildhall messages ack steer-1 ====
  ```

  Every text line is prefixed `| `, so no text can close the frame, and terminal control characters other than tab and newline are stripped; the text is already bounded at 16 KiB.
  Standard output is untouched, so each command's own output contract holds byte for byte.
  A store the command cannot read within the copy's [`lock_wait`](configuration.md#settings), or no copy found that runs the attempt, prints one `guildhall: warning: steering messages to attempt <attempt> not read: <reason>` line instead, and the command runs as normal: the read never fails the command it precedes.
- **Shown after each tool call, on Claude.**
  The Claude Artisan's settings register a PostToolUse hook for every tool, `guildhall messages hook` (`claude.SteeringHookCommand`, 10-second timeout), so the model sees a message straight after its next tool call without running a `guildhall` command.
  With unacknowledged messages it prints one Claude Code hook document, `{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"..."}}`, whose context is a line saying how many are unacknowledged and that the result waits on their ack, then each message in the frame above; with none it prints nothing.
  It shows each message after every tool call until it is acknowledged.
  It reads the store only, takes no standard input, and always exits 0: no attempt, a store it cannot read, or anything else wrong prints nothing on standard output, at most one `guildhall: warning:` line on standard error, so Claude Code adds and blocks nothing.
  A missing or failing hook therefore changes nothing about correctness: command output and intake still deliver and enforce.
- **Acknowledged by the Artisan.**
  The Artisan acts on a message and runs `guildhall messages ack <id>`.
  The Claude adapter's instruction says so, and grants `Bash(guildhall messages:*)`, which the [Artisan forms](#commands) confine to the attempt's own inbox ([adapters](adapters.md#artisan-permissions)).
- **Enforced at intake.**
  Intake refuses a succeeded result while any message to the attempt is unacknowledged, with the reason `unacknowledged steering message(s) to this attempt: <ids>; read each with guildhall messages, acknowledge it with guildhall messages ack <id>, then submit again`, naming up to ten ids.
  The check is in intake's shared check half, so `guildhall result check`, `guildhall result submit` and intake give the same reason, and a result written directly, without `submit`, is refused too.
  `result submit` spends none of its resubmissions on this rejection, since acknowledging clears it.
  A failed result is not refused for unread messages, and a message sent after the result is written but before intake is refused like any other unread message: the attempt fails, and the Director sees why.

The Director's [`guildhall-director-inbox`](../skills/guildhall-director-inbox/SKILL.md) skill says how to steer an attempt and that its result waits on the ack.

### Guildhall-sent steering

Guildhall itself steers an attempt through its outbox (`interp.GuildhallOutbox`), as sender `guildhall`, only with a type in the registry `store.ArtisanMessageTypes`; the outbox refuses any other type to an `artisan:` inbox and sends nothing.

| Type | What it tells the Artisan |
|---|---|
| `nudge` | Finish the stage's work, such as writing or submitting the result, before the session ends. |

The types are vocabulary only: Guildhall does not act on them, and the Artisan acknowledges each like any steering message.
A type is added by the change that sends it; the Director's `messages send` may use any type.

Steering covers stage attempts on the `claude` and `fake` runtimes; the fake's `ack` [directive](adapters.md#scenarios) acknowledges as a model would.
Codex runs no stage step, so it has no steering, and a quality judge has no steering inbox.

## Waking the Director

A message to the Director is stored first and wakes second: storing never depends on waking, and a message that wakes nobody is still in the inbox.
After every send to `director`, by the engine's outbox (`task start`, `step done`, `step retry`), by an attempt's launcher sending [`worker-stalled`](artisan.md#heartbeat) or [`advance-failed`](running.md#the-watcher) or by `messages send ... director`, the sending command decides whether to wake the Director and records that decision; nothing waits in the background (R1.29).
The session supervisor's `herdr-workspace-left-open` report, sent by the [workspace close sweep](artisan.md#workflow-artisan-workspaces), is the exception, as is its daily prune's `prune-workspace-kept` report ([pruning](running.md#pruning)): it is only queued unread and never wakes, so the first sweep after an upgrade cannot flood the Director's pane; the Director reads it at its next turn.
Its `hold-released` report, sent by the [holds watcher](director.md#holds) once for each released hold, does wake the Director, through the same wake as any send.
An advance records its sends under the task lock and wakes for them, one wake per message in send order, after releasing the lock, whether or not the advance succeeded, so the lock is held for records and launches only; the others wake straight after the send.
Each wake is bounded by ten seconds, and each `herdr` or endpoint call within it ends with its context ([herdr](contracts/herdr.md)), so a slow or hung pane or supervisor holds neither the task nor the sender past that bound; a wake past it is recorded `queued` with the error.

The command reads the Director's newest [session](store.md#director-sessions).
When it is `running` or `recovering` and has a bound pane, the command wakes the Director with one line and records `woke`.
How the line reaches the Director depends on the runtime the session records ([decision](decisions/director-wake-hook.md)):

- A Claude Code session is never typed into, whatever its input box holds. Its runtime keeps an async rewake hook, `guildhall director wait-wake`, armed with the session's supervisor; the command hands the line to that waiter over the session endpoint, and the waiter prints it and exits 2, which wakes the Director without touching the input box.
  A burst of messages, such as an advance's sends, wakes the Director once: the command asks the supervisor to hold its wake, which it accepts at once while a waiter is registered, and the command records `woke` without waiting, so R1.29 holds. The supervisor releases one line once no message wake has arrived for [`wake_quiet`](configuration.md#settings) (3 seconds by default), or [`wake_max_delay`](configuration.md#settings) (15 seconds) after the first, checking each second, with the unread counts from the store at the release and every held wake's handoff suffix. A lone message waits `wake_quiet` too, whatever its type; `wake_quiet = "0s"` hands every wake at once. A release that finds no waiter is a `no waiter` wake as below, and a release with nothing left unread and no handoff due wakes nobody. While a wake is held the Director does not count as idle, so a stop or reload waits for its line. The resume wake and the idle trigger are never held.
- Only the fake runtime, which is test-only and has no hooks, has the line typed into its pane, which Guildhall created for the Director's session (R1.54), with Enter.

The line:

```text
guildhall: 3 unread (attempt-failed 1, question 2)
```

The line counts the Director's unread messages by type, in type order, and never carries message text, so no sender can type instructions into the Director's session.
Notes are counted by [sender](#senders), one entry per sender in sender order, each `note <n> from <sender>`:

```text
guildhall: 1 unread (note 1 from guildmaster)
guildhall: 3 unread (merged 1, note 1 from artisan, note 1 from guildmaster)
```

The sender is Guildhall's own word from the fixed vocabulary, never text a sender chose; a note with no recorded sender reads `note <n> from unknown`.
A line with no unread note is unchanged.
Because a sender chooses its message's type, the line names only the types in `store.MessageTypes`, the one registry of the types Guildhall sends, each with its sender: those the engine and the launcher send (`advance-failed`, `attempt-failed`, `attempt-retried`, `branch-kept`, `budget-refused`, `changes-requested`, `checks-failed`, `checks-passed`, `checks-pending`, `checks-missing`, `flake`, `decision`, `dependency-held`, `director-stage`, `finished`, `loop-exit-rejected`, `merge-held`, `merge-refused`, `merged`, `slice-delivered`, `published`, `stall`, `worker-stalled`, `workspace-kept`, and the [merge queue](interpreter.md#the-merge-queue)'s `merge-queued`, `rebased`, `rebase-escalated`, `rebase-stuck` and `approval-carried`), those a [judge](quality.md) sends (`judge-failed`, `judgement`, `judgement-refused`), the session supervisor's `herdr-workspace-left-open`, `prune-workspace-kept` and `hold-released`, the Steward supervisor's `triage-opened`, [`task cancel`](tasks.md#task-cancel)'s `dependency-cancelled`, id `<repo>.<task>.dependency-cancelled.<cancelled repo>.<cancelled task>` for each open dependent, the [task page](web.md#answering)'s `decision-answered` and `grill-answered` ([grill rounds](web.md#grill-rounds)), the Steward's [`steward-report`](#steward-messages), and the local `question` and `note`; every other type counts as `other`, and `DirectorUnread` counts by the same registry.
An external message, such as a Steward message, is counted by type and sender like a note, `steward-report <n> from steward`, its type `other` unless the registry names it.
The engine sends `dependency-held` while the [dependency gate](interpreter.md#the-dependency-gate) holds a task, once per state of its unmerged dependencies; it is not the between-Directors [`dependency-notice`](#message-types).
It sends `branch-kept`, id `<repo>.<task>.branch-kept.<step>-<n>`, at most once per Artisan attempt, when the attempt's untouched task branch could not be fast-forwarded to the current main and the attempt runs on it as it is ([the working directory](interpreter.md#the-working-directory)); a branch with commits of its own sends nothing.
It sends `flake`, id `<repo>.<task>.flake.<step>.<first 12 hex digits of the head>.<8 hex digits of the check name's digest>`, under [`flaky_checks`](configuration.md#repositories) `rerun-and-file`, once per check that failed and then passed on its one [rerun](interpreter.md#validate-reruns) on a head, before that head's `checks-passed`; its first line, `policy: rerun-and-file`, is the policy the Director's inbox skill acts on, and the rest is the evidence: check, platform, job, failed and passing runs, failing test names and the `guildhall ci log` command.
It sends `finished`, id `<repo>.<task>.finished`, once, when a task whose workflow has no deliver step, such as a hallmark, ran every step and is done with no pull request, naming its last step's output ([report-only workflows](interpreter.md#report-only-workflows)); it is never a `merged` message, so it counts toward no hallmark cadence.
It replaces the dependency gate's own once-per-task message, whose retired type now counts as `other` for any such message still in a mailbox ([decision](decisions/branch-kept.md)).
The merge queue's types are the engine's: `merge-queued` gives a task's place in its repository's queue, or says its merge is held, once per place and front; `rebased` that Guildhall rebased a head onto the base tip and pushed it to the same pull request; `rebase-escalated` that a rebase became a build send-back; `rebase-stuck` that the front rebased three times in one stay and waits for the Director; and `approval-carried` that an earlier merge approval carried over to a rebased head under `reapprove_after_rebase`.
The engine and the launcher refuse to send a Director message whose type is not in the registry, so a type is added to the registry by the change that sends it and never by a sender; a local sender's `messages send` still chooses any type, which counts as `other`.
When an [automatic handoff](director.md#automatic-handoff) trigger holds, the line ends `; handoff due (<reason>)`, such as `; handoff due (context 72%)`, and the wake records the crossing, so the reminder rides on a line the Director reads anyway; the reason is Guildhall's own text, never a sender's.
Otherwise nothing is delivered and the wake is recorded `queued` with its reason, and the message waits for the Director's next turn:

| Reason | When |
|---|---|
| `no session` | No Director session has been recorded. |
| `paused`, `stopping`, `stopped` | The newest session is in that state; a paused Director is not woken. |
| `no pane` | The session has no bound pane: it was started outside Herdr, or without a complete `HERDR_*` pane ([start](director.md#start)). |
| `nothing unread` | The Director has no unread message: this one was acknowledged before the wake counted unread messages. |
| `no waiter` | A session that is not typed into had no waiter registered with its supervisor, or no supervisor listening. |
| `runtime not running` | The pane's shell is idle, so the Director's runtime has ended and the line would reach a shell prompt. |
| `pane: <error>` | Herdr refused or failed the typing, or the endpoint failed the hand-off, for example a closed tab. |

A wake recorded `no waiter` is not lost: while no waiter is registered and messages are unread, the supervisor shows a wake notice in the pane's Herdr title, outside the input box, such as `guildhall: 2 unread (note 2 from guildmaster)`, and once a waiter registers it hands it one wake of the current counts and clears the notice.
The notice is also cleared when nothing is unread.

A message is woken for at most once: the wake records the mutation `wake:<id>`, and a retried send finds the recorded wake and wakes nothing new.
The line is delivered before the decision is recorded, so a send retried after a crash between the two may deliver it again.
A failed wake never fails the send; the command reports the wake's failure on standard error, and `messages send` still prints the receipt as usual.
The pane is reached through the Herdr session it was recorded in, with `herdr` found on `PATH`, and the endpoint at `supervisor.sock` in the session's recorded directory.

The separate [resume wake](director.md#pause-and-resume) leaves each message's recorded wake unchanged.

Every wake records its sender type: `guildhall` for the engine, `local` for `messages send`, `page` for the [task page](web.md#answering) and `steward` for `messages receive`.
The message's own [`sender`](#senders) is the finer field, telling the Guildmaster, a Director and an Artisan apart.
Every sender type wakes on every message, through the coalescing above; there is no wake budget ([decision](decisions/steward-messages-directors.md)).

## Surfacing a note

Guildhall itself shows the Guildmaster a note sent to the Director, outside the model and with no model tokens, before the Director's turn starts, so the Guildmaster sees it whatever the Director does with it.
After `messages send ... director` stores a message, and before it wakes the Director, it surfaces every message of type `note` and every message whose sender is `other-director` with one Herdr toast on the Herdr server of the Director's newest session:

```text
herdr notification show "guildhall: note from guildmaster about d32" --body="Start d32 now · guildhall messages read d32-guildmaster-request-1"
```

- The title names the message's sender and task: `guildhall: note from <sender> about <task>`.
- The body is the text's first line, then ` · guildhall messages read <id>`, the read-only command that shows the whole message.
  The first line has every control character dropped, ESC and the rest of C0 and C1 among them, and is cut to 120 characters ending in `…`.
- The toast goes only to the Guildmaster's screen. It never goes through the wake, which still carries counts only, so the Director's model never sees the text from it.
- It is skipped, silently, for any other message, for a resend already woken for, and when the newest session is stopped or has no Herdr pane.
- A Herdr failure prints one `guildhall: messages send: surface: <error>` line on standard error; it never fails the send or stops the wake. The call is bounded by the wake's ten seconds.
- Surfacing never acknowledges the message, and nothing else is printed.

The toast is transient.
The note's sender also shows in `director status`'s message headers and in `messages read`.
The toast is used rather than the pane title, which belongs to the supervisor's wake notice, and rather than the Lanterns status line, which refreshes only after an assistant message, too late to show the note first.
Routine engine messages are never surfaced.

## Steward messages

The Steward tells an enrolled Director about an issue it found with a **Steward message**, built from the [Director messaging](#director-messaging-designed-not-built) design ([decision](decisions/steward-messages-directors.md)).
It informs only, under the [authority](#authority) rule: it is not a command, dispatch, a task, an approval or a push of configuration, and at most becomes a ticket that opens a Guildmaster decision; only the Guildmaster's `accept` creates a task.
The Steward's supervisor sends it from its scheduled run, never its model ([Steward messages](steward.md#steward-messages)); no Director reaches the Steward in return.

### External messages

A Steward message is an **external message**: a new store record in the receiving Director's store, keyed by its sender and id outside any repository, so it names no task of the store.
The local `guildhall.message/1` inbox and its commands are unchanged.
Its envelope is `guildhall.director-message/1`, decoded with the local envelope's strict rules, a byte order mark and CRLF accepted and UTF-16 refused with a hint:

```json
{
  "schema": "guildhall.director-message/1",
  "id": "rollout-failed.3f9a0c1d2b4e5f60",
  "from": "steward",
  "to": "tools",
  "type": "steward-report",
  "kind": "rollout-failed",
  "subject": "Rollout 1a2b3c4d5e6f to this copy ended reload_failed",
  "text": "The Steward's newest rollout to this copy ... ended reload_failed: ..."
}
```

| Field | Contract |
|---|---|
| `id` | Chosen by the sender, unique among its messages: the local id rules. |
| `from` | `steward`; it must match the sender `messages receive` records. |
| `to` | The receiving copy's Director name, its config's `name`. |
| `type`, `kind` | `steward-report`, with the `kind` `rollout-failed` (the Steward's newest rollout to the copy failed), `unreachable-recovered` (the copy was unreachable for a configured span and is reachable again), `director-stuck` (no Director turn for a configured span while messages waited), `director-unhealthy` (the session's supervisor is gone, or the session stayed recovering) or `release-behind` (the session runs a release older than the copy's pin), the last three from the Steward's [Director health](steward.md#director-health) checks. Only a `steward-report` has a `kind`. |
| `subject` | The one-line header a summary shows: non-blank, no line break, at most 200 bytes. |
| `text` | As in `guildhall.message/1`. |
| `about`, `artifacts` | Optional, as designed; `in_reply_to` is refused until a Director's answer is built. |

A listed or read external message is the record: the fields above with `sender`, `sequence`, `received_at` and `acknowledged`.
Its sender is `steward`, set by `messages receive` and never by the envelope or by `messages send`.

### messages receive

`guildhall messages receive --copy <dir>` is the fixed remote command the Steward runs after copying one envelope with `scp` to `state/steward/message.json` in the copy; the Steward delivers one message at a time per copy.
It reads the file, records the message from `steward`, deletes the file, and prints one JSON document, `guildhall.receive-result/1`:

- `{"status": "delivered", "receipt": {...}}` once the message is stored. The receipt's mutation is `dmessage:steward:<id>`, resolved first: an identical message received before returns its original receipt, even when the inbox is now closed or full, and wakes nothing again.
- `{"status": "refused", "reason": "..."}` for an invalid envelope, one addressed to another Director, one that reuses an id with other content (`conflict`), and, for a new message only, an inbox [closed](#closing-the-inbox-to-a-sender) to the Steward (`closed: ...`) or a [full mailbox](#a-full-mailbox) (`mailbox full: ...`). A refusal stores nothing and is final: the Steward records it and never retries.

Both exit 0.
It exits 1, keeping the file, when there is no file to read or the store or config fails, and the Steward retries it.
A new message is stored first, then [surfaced](#surfacing-a-note) and woken for: one Herdr toast titled `guildhall: steward-report <kind> from steward`, its body the sanitised subject and `guildhall messages read --sender steward <id>`, then the usual wake, whose line counts it, `steward-report 1 from steward`, and carries no text.

### Reading and acknowledging

The Director reads external messages by their full identity, and never by a guessed sender:

- `guildhall messages summary` prints the unread external messages counted by sender, each sender's inbox state and the headers (`sender`, `id`, `type`, `kind`, `subject`, `sequence`) of the newest 20 unread, newest last; never their text.
- `guildhall messages list --from steward [--after <sequence>] [--limit <n>]` lists them in receive order, each with its recorded wake.
- `guildhall messages read --sender steward <id>` prints one, acknowledged or not, without acknowledging it.
- `guildhall messages ack --sender steward director <id>` acknowledges it with the mutation `dmessage-ack:steward:<id>`, once the Director has dealt with it.

Without `--from` or `--sender`, `list`, `read` and `ack` keep their local meaning.
The Director tells the Guildmaster what a Steward message says when it matters, and acts on none of it on its own ([inbox skill](../skills/guildhall-director-inbox/SKILL.md)).

### Closing the inbox to a sender

`guildhall messages inbox close --sender steward --reason <text>` closes the Director's inbox to that one sender: its new messages are refused `closed` until it reopens, and other senders are not affected.
Closing opens an alert on the reserved task `messages/inbox-steward`, a `question` decision `inbox-steward-<n>` for the n-th close with the options `reopen` and `keep-closed`, which `decide list` and `needs_me` show and only the Guildmaster answers: `decide answer` refuses it inside any session.
`guildhall messages inbox open --sender steward` is refused unless the Guildmaster answered the newest close's alert `reopen`.
The repository id `messages` is reserved and refused for a real repository; task listings leave it out.

### A full mailbox

[`mailbox_max`](configuration.md#settings), 200 by default, caps the unacknowledged external messages; at the cap a new message is refused `mailbox full`, and one sent after the Director acknowledges some is taken.

### The outbox

A sender keeps each message in its own store's outbox: `pending` when recorded, then `delivered` with the receiver's receipt or `refused` with its reason, each final.
A delivery that fails, such as an unreachable or sleeping machine, stays `pending` with its error, and the sender's session supervisor retries it on its schedule, deterministic and with no model call.
`guildhall messages outbox [--all]` lists the entries not delivered, or every one; it is the Steward's in a Steward copy.

## Director messaging (designed, not built)

Everything in this section is designed, not built, except the [local wake](#waking-the-director) and its wake record.
No command, flag, config key or other store record below exists yet; later slices build each contract red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [Director messaging](decisions/director-messaging.md).
It extends the local inbox above so a Director can hear from the Guildmaster and from other Directors, without giving any message more authority than the local inbox has today.

### Who may send

A Director's inbox has two external sender types:

| Sender type | Who | Default wake |
|---|---|---|
| `guildmaster` | The Guildmaster, by their own command | Always wakes |
| `director` | Another Director, on this machine or another | Draws on the [wake budget](#waking-a-director) |

The Steward is a third external sender, `steward`, and is built: it informs an enrolled Director of issues it finds with a [Steward message](#steward-messages), which draws on no wake budget.
It changes a Director only by command: it copies `managed.toml` as rollouts do today and may then run `guildhall config reload` on the copy (see [Standalone and managed Directors](director-modes.md#how-the-steward-acts)).
The Director's binary records each managed-settings revision it applies as a store event, which the task timeline and the status feed show; a revision itself never enters the inbox, though a failed rollout's report does.

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

The wake budget and the `wake_director*` keys below are dropped ([decision](decisions/steward-messages-directors.md)): every external message wakes through the local wake's coalescing, as a [Steward message](#steward-messages) does, and flooding is handled by [closing the inbox to one sender](#closing-the-inbox-to-a-sender), which replaces the per-inbox close of the next section.

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

### Maintenance tickets to the Steward

The [rails, not tracks decision](decisions/rails-not-tracks.md) extends the ticket model with Director-filed maintenance incidents: engine dead ends, unexpected states and workarounds, including incidents the Director resolved successfully.
This is a separate maintenance intake, not a new sender or recipient accepted by today's local inbox or by the Director-to-Director envelope above.
The existing proposed `ticket file --from-message` form admits only `handoff-offer` and `dependency-notice`; it does not file maintenance incidents.
Local filing, listing and triage are built as the `incident` command, named so because `director maintenance` already means note audit and hallmark and `ticket` is the designed Director-messaging command; Steward [collection](steward.md#collection), its schedule and the Steward's [triage](steward.md#triage) are built too.

- `guildhall incident file <file|->` files a strict-JSON report, schema `guildhall.incident/1`, with the fields below, as `open`.
  Filing the same report again returns the same receipt, and another report under the same id is refused; a report over 64 KiB is refused with its size.
- `guildhall incident observe <id> <file|->` links later evidence, schema `guildhall.incident-observation/1` with `observed_at`, a `note` and `evidence`, as the incident's next observation; it never overwrites the report.
  An incident and its observations together hold at most 512 KiB, so it always fits in a pending list; an observation past that is refused with the size, and a new incident that names the old one carries further evidence.
- `guildhall incident list [--json] [--pending]` shows the local queue, `--pending` only incidents not yet collected, and the JSON form each incident with its observations and the copy's queue origin; a pending JSON list is capped at 1 MiB, with `truncated` set when incidents were left for a later list.
- `guildhall incident resolve <id> accepted|deferred|declined` records the Guildmaster's local decision; a deferred incident may later be accepted or declined, and a `collected` one is refused, because from then on the Steward's queue owns it.

These commands find the copy without configuration admission, so an incident about an untrusted managed digest can still be filed, and they refuse a Steward copy ([store records](store.md#lessons-and-incidents), [commands](contracts/cli.md#lessons-and-incidents)).

The incident record has:

- a stable originating Director identity, the copy's random queue origin, and the ticket `id`; `repository`, `task`, `step` and `attempt` references, each `"n/a"` for a pre-task failure;
- `release` and `workflow` revisions, the `observed_state` and `observed_at` time, the exact `error`, and `evidence` references, each a `ref` with its `digest`, `sha256:<64 hex>`;
- what the Director `tried`, what it actually `done`, the `workaround` if any, and the remaining `risk`;
- collection state and receipt, `collected` with the private-repository commit, plus a link to the Steward's queue item when collected.

Evidence capture must exclude credentials and unrelated private data; large evidence stays referenced rather than copied into a message body.
The queue is durable locally before any collection, and repeated delivery of the same origin/id returns the same receipt; conflicting content under that identity is refused, while later observations are linked as new evidence rather than overwriting the original.
The local record remains until collection has a durable receipt.
Transport failure leaves the ticket pending locally, never silently discarded or described as collected.

The Steward [collects](steward.md#collection) proposed maintenance tickets with `guildhall steward collect`, on request or on its optional `collect_every` schedule, over its existing SSH access to enrolled Director copies, as for [lessons](director-modes.md#lessons), and persists its maintenance queue in the private configuration repository as `incidents/<machine>/<origin>-<id>.json`.
Directors need neither that repository nor a connection to the Steward, and collection adds no sender to a Director's inbox.
A collection receipt is a command result, the file `director collected` reads, not a [Steward message](#steward-messages), acceptance of the proposed work or permission to act.
A standalone Director uses the identical local queue for its Guildmaster to triage and can have it collected later if a Steward is introduced.

The Guildmaster may accept, defer or decline the proposed maintenance work, with the decision and evidence retained.
Filing a ticket launches no Artisan, changes no policy and grants no task or merge approval; it must not block an otherwise safe continuation of the originating task.
Acceptance leads to separately scoped work under the ordinary task, approval and rollout rules; it does not retroactively approve the originating task or authorize an install.
This reuses the ordinary ticket principle that a proposal is not a task, but needs a maintenance-specific triage lifecycle and a subject independent of an existing task, as ordinary tickets already require.
Filing, listing, evidence references, local triage and the collected mark are built and contract-tested in standalone and managed copies, and so are collection, private-queue persistence, offline retry and the Steward's triage, against stubbed `ssh` and `scp` and a local Git remote; collection's live acceptance waits for the Guildmaster's d96 run.
Nothing polls or spends model tokens merely because a ticket is queued.

### Conflicts to resolve

These are flagged rather than chosen silently:

- **R1.15** says work moves between Directors only by manual handoff with no automatic cross-Director scheduling; a ticket schedules nothing and needs the Guildmaster's answer, but the requirement does not yet mention Director messages.
- **R1.20** says there is no push service; a wake is a push into a pane by the `receive` command, with no service.
- **R1.8** sets up SSH from the Steward to Directors only; Director-to-Director transport needs SSH between Director machines.
- **[Local messaging](decisions/local-messaging-in-the-store.md)** deferred messaging another Director, transports and notifications; this design is that follow-up and extends it rather than replacing it.
- **[Scp rollouts](decisions/steward-rollouts-copy-with-scp.md)** says no remote command is run; `messages receive` over SSH is a Director's remote command, not a rollout's, and does not change rollouts.
