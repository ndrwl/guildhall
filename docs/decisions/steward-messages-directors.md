---
status: accepted
date: 2026-11-04
topic: messaging
tasks: d104, d129
amends: director-messaging.md, optional-steward.md, steward-fixed-remote-commands.md
---

# The Steward tells an enrolled Director about issues it finds with a Steward message, which informs only

## Context

[Director messaging](director-messaging.md) said the Steward never messages a Director and acts only by command, and [optional Steward](optional-steward.md) said the Steward acts by command.
The Guildmaster asked for Steward-to-Director messages "when issues are found" (d96, d104, d129), such as a failed rollout, with the authority rule unchanged.
The Steward's `Runner` passes no standard input, so the designed `messages receive ... -` over SSH does not fit it.

## Decision

The Steward may send an enrolled Director a **Steward message**, type `steward-report` with a `kind` (`rollout-failed`, `unreachable-recovered`), in the designed `guildhall.director-message/1` envelope, from its supervisor's scheduled run and never from its model ([Steward messages](../messaging.md#steward-messages)).
It informs only: it is not dispatch, a task, an approval or a push of configuration, and at most becomes a ticket that opens a Guildmaster decision; only the Guildmaster's `accept` creates a task.
The Steward copies the envelope with `scp` to the fixed drop file `state/steward/message.json` in the copy and runs one new fixed command, `guildhall messages receive --copy <copy_dir>`, as collection does; this amends [the allow-list](steward-fixed-remote-commands.md) for that one command, which keeps its one-variable shape.
`messages receive` records the sender `steward`, which no envelope or environment chooses and `messages send` never records, in a new external-message record keyed by `(sender, id)` outside any repository.
It resolves an identical retry before admission, then refuses a new message while the Director has closed its inbox to that sender or holds `mailbox_max` unacknowledged; a refusal is final and never retried.
The Steward keeps each message in its store's outbox, `pending` until delivered or refused, and its scheduled run retries pending ones.
There is no wake budget: a Steward message wakes through the local wake's coalescing and gets the same Herdr toast as a note.
Closing the inbox is per sender and raises a `reopen`/`keep-closed` alert on the reserved task `messages/inbox-<sender>` that only the Guildmaster answers.
The rest of [Director messaging](director-messaging.md), Director-to-Director messages and tickets, stays designed.

## Alternatives rejected

- Standard input over SSH, as designed for Directors: it widens the Steward's `Runner`, and the drop file has collection's proven shape.
- A wake budget per sender: the Guildmaster chose no daily or count limit; closing the inbox to one sender handles flooding.
- A per-inbox closed state: one flooding sender would silence every other.
- Retrying only on request: a message would wait while a laptop sleeps.

## Consequences

The Steward session still never reaches a Director by itself, and no Director reaches the Steward's inbox.
A Steward message reaches only enrolled copies; standalone copies need no Steward and get none.
The reserved repository id `messages` is refused for a real repository.
Steward messages are sent only while `[steward] collect_every` schedules a run.
