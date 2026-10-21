# Loops and the review gate from recorded bindings

Every Artisan attempt records what it was given, its inputs and digests and for a review the revisions its outcome must be bound to, as a document `<step>-<n>-inputs` before the attempt itself.
The engine still derives its position from records alone: an attempt stands while its wired inputs and revisions are unchanged, a loop is taken when its target has not yet been given the blocking result as feedback, and a loop's rounds are the step's blocking results, so no loop counter or goto marker is stored.
A binding is a document rather than a new store record because `interp.Records` already writes documents and a new record type would widen the store for one consumer; the alternative, replaying the whole history to infer order, would make every rule depend on the order of earlier rules.
The review step gets its own previous outcome on port `previous` and the loop's target gets each blocking result on port `feedback`, so findings travel through records, never transcripts (report §5.2).
The [rounds and loops contract](../interpreter.md#rounds-and-loops) defines blocking results and exit answers; review acceptance uses the shared review outcome contract so the review gate needs no waiver path.
Stall signals are Director messages and never change a loop (report §5.3); the [rounds and loops contract](../interpreter.md#rounds-and-loops) defines when a loop exit asks the Guildmaster.
The [review gate](../interpreter.md#review-gate) re-checks outcomes from records at `deliver`, independently of the walk's stale-attempt handling, so a later change to the walk cannot bypass it.
Loops back to Guildmaster, Director or engine steps and rebase-aware re-review are later slices.
See [Director rulings](director-rulings.md) for the subsequent ruling and heartbeat slice.
