---
status: accepted
date: 2026-10-26
topic: engine
---

# Rejected result intake is recorded separately from launcher completion

## Context

An Artisan's process success does not establish that its outputs satisfy the step's contract.
Using `FinishAttempt` to record rejected intake cannot correct a launcher-succeeded attempt, because the first finish stands, leaving normal retry unavailable.

## Decision

Add `RejectAttempt` to the interpreter's existing consumer-owned `Records` boundary, rather than weakening `FinishAttempt` or rewriting the launcher's historical receipt.
The [store's attempts contract](../store.md#attempts) owns the mutation, its audit history and replay semantics.
The [interpreter's replay and recovery contract](../interpreter.md#replay-and-recovery) owns which validation failures qualify, operational-error recovery and Director notification.

## Alternatives rejected

- Hand-writing a missing verdict: it would conceal the invalid result rather than repair the recovery path.
- Weakening `FinishAttempt` so a later finish overrides the first, or rewriting the launcher's receipt: both lose the launcher's history.
- A new workflow outcome, status-feed kind or retry command: the existing failed-attempt and retry flow already serves.

## Consequences

A rejected intake reuses the existing failed-attempt and retry flow.
The [interpreter regression tests](../contracts/interp.md#tests) cover launcher success followed by rejected intake and normal retry; the [store contract tests](../contracts/store.md#tests) cover preservation of launcher history.
