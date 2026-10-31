---
status: accepted
date: 2026-10-20
topic: messaging
formerly: D15, 0015-local-messaging-in-the-store.md
---

# Director and Artisan messages are records in the Director's store

## Context

Directors and Artisans need to leave each other messages, and the Director already has a store with replay and conflict rules.
There was no Director or Artisan registry yet.

## Decision

Messages are records in the Director's existing store.
A send is a store mutation whose id is derived from the message id, so the message, its inbox entry and its receipt commit in one transaction, and the store's replay and conflict rules give retries and id reuse their meaning.
The `messages` and `inbox` buckets are per-repository buckets created on first use, like the other record buckets, so the store stays at schema 1; no release has shipped a schema 1 reader that lacks them.
An Artisan's inbox is named by the running stage attempt it works on; a later registry can map onto that identity rather than replace it.
The command line takes the store path explicitly and the envelope from a file or standard input.
The behaviour is described in [Messaging](../messaging.md).

## Alternatives rejected

- A separate database, queue, server or file drop: the store already gives atomic commits, replay and conflict rules.
- A second deduplication scheme: the store's mutation ids already make retries safe.

## Consequences

Runtime notifications, daemons and transports remain deferred; [Director messaging](director-messaging.md) is the designed follow-up and extends this record rather than replacing it.
