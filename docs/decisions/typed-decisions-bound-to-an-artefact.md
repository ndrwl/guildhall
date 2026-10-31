---
status: accepted
date: 2026-10-20
topic: store
formerly: D23, 0023-typed-decisions-bound-to-an-artefact.md
---

# A typed decision is bound to the artefact its answer applies to, with option-checked answers

## Context

A Guildmaster answer had to apply to one exact artefact, such as a spec, and the Director had to read a choice from it without parsing prose.

## Decision

The [typed decision record](../store.md#records) identifies the artefact an answer applies to, providing the subject binding consumed by the [interpreter](../interpreter.md).
Option-checked answers let the Guildmaster answer in words and the Director read a choice; the store reference owns the exact matching and note rules.
The [command reference](../decisions-cli.md#answers) defines input normalization, so the same words record the same answer on Windows and elsewhere.
Answer mutation ids use `:` because `/` is not a mutation id character; the command reference owns their encoding and length fallback.
Recording an answer never advances a task; see the [interpreter events](../interpreter.md#events) for how recorded answers are processed.

## Consequences

The new fields are optional in the record, so decisions written before them decode and take any answer, and the answer request is unchanged, so their receipts still replay; the store stays at schema 1.
