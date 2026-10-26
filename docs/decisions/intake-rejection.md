# Record rejected result intake separately from launcher completion

An Artisan's process success does not establish that its outputs satisfy the step's contract.
The interpreter must reject missing or invalid outputs, unreadable files named by the result, and invalid port content as an attempt failure, with a durable reason.
The task then waits for the Director, who receives one `attempt-failed` message naming the attempt and reason and may retry normally.

Add `RejectAttempt` to the interpreter's existing consumer-owned Records boundary.
This mutation accepts an unfinished or launcher-succeeded attempt and records `failed` with `intake_rejection`; failed or stopped attempts cannot be overwritten.
The distinct `reject-attempt` receipt preserves the earlier launcher `finish-attempt` receipt in history rather than rewriting it.
The mutation also releases ownership, as finishing an attempt does.
Identical mutation replay returns the original receipt; a changed request conflicts.

Only errors positively identified at the result-validation boundary qualify.
Store errors, interrupted execution, missing or unreadable Guildhall-owned binding documents, and failures writing documents or intake markers remain replayable errors, not rejection.
A result-file read failure remains replayable; malformed or wrong-identity result bytes are rejected, while unsupported newer result versions remain operational errors requiring a compatible Guildhall.
Opening the task working directory is an operational error, but failure to read an output explicitly named by an Artisan is rejection.
Code-head derivation failures retain their existing recovery behavior.

A succeeded attempt without an intake marker is validated on the next advance, including legacy attempts already wedged at launcher success.
A rejected attempt's stored reason drives the existing failed-attempt notification path, so a crash before notification replays the notification without duplicating it.
No new workflow outcome, status-feed kind, or retry command is needed.
