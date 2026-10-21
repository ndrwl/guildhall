# Loops and the review gate from recorded bindings

Every Artisan attempt records what it was given, its inputs and digests and for a review the revisions its outcome must be bound to, as a document `<step>-<n>-inputs` before the attempt itself.
The engine still derives its position from records alone: an attempt stands while its wired inputs and revisions are unchanged, a loop is taken when its target has not yet been given the blocking result as feedback, and a loop's rounds are the step's blocking results, so no loop counter or goto marker is stored.
A binding is a document rather than a new store record because `interp.Records` already writes documents and a new record type would widen the store for one consumer; the alternative, replaying the whole history to infer order, would make every rule depend on the order of earlier rules.
The review step gets its own previous outcome on port `previous` and the loop's target gets each blocking result on port `feedback`, so findings travel through records, never transcripts (report §5.2).
A result is blocking only when it requests changes with a finding that cites a rule; ruleless findings are suggestions and never loop (audit C7).
Exhausting `max` opens a `loop-exit` decision instead of failing, and `continue` adds one round per answer; `accept` records the Guildmaster's approval in the shared review outcome contract, so the review gate needs no waiver path.
Stall signals are Director messages and never change a loop: only `max` asks the Guildmaster (report §5.3).
The review gate at `deliver` re-checks from records that every code review step has a non-blocking outcome bound to the delivered head, spec and current standards revision, even though the walk already re-runs stale reviews, so a later change to the walk cannot bypass it.
Loops back to Guildmaster, Director or engine steps, the worker heartbeat, Director rulings and rebase-aware re-review are later slices.
