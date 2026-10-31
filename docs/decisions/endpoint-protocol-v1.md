---
status: accepted
date: 2026-10-21
topic: endpoint
formerly: D38, 0038-endpoint-protocol-v1.md
---

# The attempt control protocol is versioned and decoded strictly

## Context

`stop` and `cancel` on the [attempt endpoint](attempt-endpoint-af-unix.md) are destructive.
A launcher and the process calling it can come from different Guildhall binaries once one outlives an update.

## Decision

Request and reply lines carry `"schema": "guildhall.attempt-control/1"` and are decoded strictly ([protocol](../artisan.md#protocol)), so a request whose meaning this endpoint might not share is refused rather than guessed at.
Before any identity check or handler call, the endpoint refuses a newer version, a missing or malformed schema, a duplicate key and a field version 1 does not declare, at any depth, and `Call` refuses such a reply with `ErrEndpointProtocol`.
A later version will be served only by naming it in the endpoint; the line framing, the 4 KiB request and 1 MiB reply bounds, the transport bounds and the exact attempt and launcher match are unchanged.
Transport I/O and the wait for a handler's reply have separate bounds, not one whole-call exchange deadline (sustainability review 12, F43); the [shared endpoint contract](../contracts/endpoint.md) owns those bounds for both attempt control and session signals.

## Alternatives rejected

- Accepting unversioned or extended requests and guessing their meaning: wrong for destructive requests across binary versions.
- One whole-call exchange deadline: an attempt's `stop` must be allowed to complete its stop ladder.

## Consequences

No unversioned client existed when this protocol was introduced; the current compatibility rules are in the [protocol reference](../artisan.md#protocol).
Serving a new version is a deliberate change to the endpoint, never something a peer negotiates.
A `Call` waiting on `stop` is bounded only by the caller's context.
