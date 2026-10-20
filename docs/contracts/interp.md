# `internal/interp`

## Contract

`Engine.Advance`, the deterministic straight-path interpreter ([interpreter](../interpreter.md)): the events, what each records, the spec gate, derived mutation ids and replay after a crash between recording and launching an attempt.
Its consumer seams: `Records` (the store methods it calls), `Prompter` with `PromptFunc`, `Outbox` (the send half of `messaging.Mailbox`) and the launch seam ([scripted mock agent](../mock-agent.md#the-launch-seam)): `Launcher`, `Spec` and its validation, `Handle`, and the `CheckAdapter` guard that refuses every adapter but `mock` and `fake` inside a test binary.
`BlockedQuestion` is the provisional file a blocked attempt writes.

## Tests

`engine_test.go` (new, red first against a stub `Advance`): the straight workflow against the mock with the in-memory records and messaging fake, and against the mock with a real store file and its mailbox; the approval bound to the plan document; the spec gate; crash between record and launch; the zero-model guard; refused loops; a failed attempt; mismatched events; a changed revision.
`TestRecordsContract` runs the shared `Records` suite against `*store.Store` and `interptest.Records`.
`launcher_test.go` (the guard inside and outside a test binary); `Spec.Validate` through the shared suite in `interptest`.
