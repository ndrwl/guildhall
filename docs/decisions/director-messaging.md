---
status: designed
date: 2026-10-21
topic: messaging
formerly: D49, 0049-director-messaging.md
---

# The Guildmaster and other Directors send a Director typed messages over SSH, which inform but never start work

## Context

The [local inbox](local-messaging-in-the-store.md) deferred messaging another Director, transports and notifications.
The Guildmaster and other Directors need to reach a Director without breaking R1.15's rule of no automatic cross-Director scheduling or R1.29's rule of no background retries.
This is designed, not built; [Director messaging](../messaging.md#director-messaging-designed-not-built) holds the full design.

## Decision

The inbox has two external sender types, `guildmaster` and `director`; the Steward never messages and acts by command, and each managed revision it causes is a store event, not a message, so a Director's inbox holds only things the Director must judge.
Messages inform only: a Director message may at most be filed as a ticket that opens a Guildmaster decision, so no Director starts work in another's repositories without the Guildmaster's answer.
Messages are typed (`handoff-offer`, `dependency-notice`, `question`, `answer`) under a new envelope schema, leaving `guildhall.message/1` unchanged.
Waking is per sender type: the Guildmaster always wakes, Director messages draw on one wake budget with backoff, and the wake line carries counts only, so no sender types into a Director's session.
Spam is handled by the receiver closing its director inbox with a system alert that only the Guildmaster can resolve, plus a mailbox cap that refuses rather than drops.
Transport is Director to Director over SSH with a forced command per sender key, so the transport, not the envelope, is the sender's identity.
Delivery is at least once, with an outbox in the sender's store and exactly-once storage in the receiver's through mutation ids namespaced by sender, the same receipts the store already gives local messages; nothing retries in the background.
The shared receive boundary validates identity and resolves an identical committed retry before closed-inbox or capacity checks, returning the original receipt without another wake; conflicting content is refused.
Read, acknowledgement and ticket filing keep the full `(sender, message id)` identity through `--sender <sender>`, and ticket source references keep both fields.

## Alternatives rejected

- Free chat between agents: it would make authority and spam hard to bound.
- A per-sender limit: the receiving agent judges content better than a sender quota, and the wake budget bounds the cost.
- Relaying through the Steward: messaging would need a Steward, and the Steward would sit in every conversation.

## Consequences

Until it is built, only the local inbox exists, and nothing here describes current behaviour.
The `guildmaster` sender type will refine the `local` sender that `messages send` records today.
