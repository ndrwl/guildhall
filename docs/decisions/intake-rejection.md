# Record rejected result intake separately from launcher completion

An Artisan's process success does not establish that its outputs satisfy the step's contract.
Using `FinishAttempt` to record rejected intake cannot correct a launcher-succeeded attempt because the first finish stands, leaving normal retry unavailable.
Hand-writing a missing verdict would conceal the invalid result rather than repair that recovery path.

Add `RejectAttempt` to the interpreter's existing consumer-owned Records boundary instead of weakening `FinishAttempt` or rewriting the launcher's historical receipt.
The [store's attempts contract](../store.md#attempts) owns the mutation, its audit history and replay semantics.
The [interpreter's replay and recovery contract](../interpreter.md#replay-and-recovery) owns which validation failures qualify, operational-error recovery and Director notification.
This reuses the existing failed-attempt and retry flow without adding a workflow outcome, status-feed kind or retry command.

The [interpreter regression tests](../contracts/interp.md#tests) cover launcher success followed by rejected intake and normal retry; the [store contract tests](../contracts/store.md#tests) cover preservation of launcher history.
