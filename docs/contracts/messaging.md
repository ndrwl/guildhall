# `internal/messaging`

## Contract

`Decode` and the envelope (`docs/messaging.md`), read with [`strictjson`](strictjson.md); `Mailbox`: durable send with receipts, retry and id-reuse conflict, recipient, task and attempt checks, bounded ordered listing and acknowledgement, concurrent use (D15).
`StoreMailbox` implements it on the store.
`Wake` is the one wake for a message to the Director ([waking the Director](../messaging.md#waking-the-director)): over the narrow `WakeStore`, which `*store.Store` implements and the commands implement by opening the store per call, and the `DirectorPane` it types through, it types `WakeLine`'s counts into a running or recovering session's bound pane and records `woke`, records `queued` with its reason otherwise, and returns the recorded wake for a message already woken for, typing nothing.
`WakeResumed` is the resume's wake ([pause and resume](../director.md#pause-and-resume)): over the narrow `SessionReader` and the same `DirectorPane`, given the work waiting by kind, it types `ResumeLine`'s counts into a running or recovering session's bound pane and returns `woke`, and otherwise types nothing and returns `queued` with `nothing waiting` or `Wake`'s session and pane reasons; it records nothing, so each message's recorded wake stands.

## Tests

`envelope_test.go` (with `testdata/` encodings); `store_mailbox_test.go` runs the shared suite against real store files, closing and reopening them for restart; `wake_test.go`, written red first against a stubbed `Wake` and `WakeLine` ([red run](../evidence/director-wake-2026-10-22-red.txt)), on real store files with a recording pane: one line of exact counts per new message, never its text, nothing typed for a replayed send after a restart, each queued reason with nothing typed and the message still in the inbox, a recovering session woken, a nil pane queued, and a missing or Artisan message refused.
Its `WakeResumed` and `ResumeLine` tests were written red first against stubs ([red run](../evidence/director-checks-watch-and-resume-wake-2026-10-22-red.txt)): one line of counts for a resumed session with work waiting, the message's queued `paused` wake unchanged, each queued reason with nothing typed, a nil pane queued, and the line's format.
`messagingtest` gains no case
