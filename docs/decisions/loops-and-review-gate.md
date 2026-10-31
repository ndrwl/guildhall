---
status: accepted
date: 2026-10-21
topic: engine
amends: engine-advance.md
amended-by: review-outcome-2.md
formerly: D35, 0035-loops-and-review-gate.md
---

# Loops and the review gate are derived from recorded attempt bindings

## Context

The [straight-path engine](engine-advance.md) refused loops and gates other than the spec gate, so that later slices could add them without changing what the straight path records.
Adding loops must keep the engine deriving its position from records alone.

## Decision

Every Artisan attempt records what it was given, its inputs and digests and, for a review, the revisions its outcome must be bound to, as a document `<step>-<n>-inputs` before the attempt itself.
An attempt stands while its wired inputs and revisions are unchanged, a loop is taken when its target has not yet been given the blocking result as feedback, and a loop's rounds are the step's blocking results, so no loop counter or goto marker is stored.
The review step gets its own previous outcome on port `previous`, and the loop's target gets each blocking result on port `feedback`, so findings travel through records, never transcripts (report §5.2).
The [rounds and loops contract](../interpreter.md#rounds-and-loops) defines blocking results, exit answers and when a loop exit asks the Guildmaster; review acceptance uses the shared review outcome contract, so the review gate needs no waiver path.
Stall signals are Director messages and never change a loop (report §5.3).
The [review gate](../interpreter.md#review-gate) re-checks outcomes from records at `deliver`, independently of the walk's stale-attempt handling, so a later change to the walk cannot bypass it.

## Alternatives rejected

- A new store record type for bindings: `interp.Records` already writes documents, and a new type would widen the store for one consumer.
- Replaying the whole history to infer order: every rule would depend on the order of earlier rules.

## Consequences

Loops back to Guildmaster, Director or engine steps were left to later slices.
[Rebase-aware review](../interpreter.md#rebase-aware-review) later added re-review support, and [Director rulings](director-rulings.md) the ruling and heartbeat slice.
[Review outcome /2](review-outcome-2.md) amended this: a blocking result is now any `changes_requested` verdict, and publish runs the review gate too.
