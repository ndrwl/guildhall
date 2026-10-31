---
status: accepted
date: 2026-10-21
topic: review
amended-by: review-outcome-2.md
formerly: D40, 0040-director-rulings.md
---

# Director rulings are store records, and a contested blocker opens the loop exit early

## Context

The Director needs `out-of-scope`, `duplicate` and `defer` rulings on review findings (report §5.4).
A ruling is a move that is made once and contested once, and both need the store's once-only and replay rules.

## Decision

A ruling is a new store record keyed by the review outcome document and the finding id, written by `guildhall task finding rule`.
Keying by document rather than step means neither the store nor the command parses record names; the engine maps the document to its step through its history view.
A review outcome's document reference projects its findings and dispositions, so the store can check a ruling's finding and task inspection can show findings without opening outcome files.
A ruling never clears a finding; the [interpreter contract](../interpreter.md#director-rulings) defines immutable ruling inputs and contest processing.
A contested rule-cited blocker opens the round's existing `loop-exit` decision early, so `continue`, `accept` and `reject` keep one meaning and `accept` satisfies the review gate through the same outcome.

## Alternatives rejected

- Rulings as messages or documents: neither has the store's once-only and replay rules.
- A dedicated dispute decision kind: it would need its own answers and a second path to the review gate.

## Consequences

The same change added heartbeat-driven [worker-stalled notifications](../interpreter.md#worker-stalled-observations).
Rebase-aware re-review came in a later slice ([rebase classification](rebase-classification.md)).
[Review outcome /2](review-outcome-2.md) amended this: an uncontested ruling on a `disputed` finding releases it for `nothing_open`, the one case where a ruling counts against a finding, and rulings may name suggestions, to which `defer` applies.
