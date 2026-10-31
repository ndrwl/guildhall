---
status: accepted
date: 2026-10-21
topic: review
formerly: D43, 0043-full-review-request.md
---

# Full-review requests are store records consumed by a round

## Context

The Director needs a way to force a full review where [rebase classification](rebase-classification.md) would narrow it, with `guildhall task review --full`.
The request is a Director move that must survive restarts, carry a receipt like rulings, and be read by the engine through the same history view as every other record.

## Decision

`guildhall task review --full` records a numbered per-task store record.
The engine consumes a request with a second mutation naming the attempt, the same shape as contesting a ruling, so the timeline shows when it was requested and which round it forced.
The attempt consumes the request before its binding is recorded; a replay finds the request consumed by the same attempt.
At most one request is pending, so a retried command is idempotent through its mutation id, which names the request number.
A request makes the round full without classifying it, but the binding still records the range.

## Alternatives rejected

- A document or a message: neither survives restarts with a receipt and the store's once-only rules.

## Consequences

A crash between consuming and binding never loses the request or applies it to a later round.
A later rebase can still be classified against the range the full round recorded.
