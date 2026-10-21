# `internal/interp`

## Contract

`Engine.Advance`, the deterministic interpreter ([interpreter](../interpreter.md)): the events, what each records, the spec gate, derived mutation ids and replay after a crash between recording and launching an attempt.
Attempt preparation ([interpreter](../interpreter.md#attempt-preparation), [decision](../decisions/immutable-attempt-preparation.md)): a prepared attempt is published once by renaming `prompt.partial` to `prompt`, replays launch from it unchanged after validating it against its manifest and recorded binding, and invalid preparation is refused with `ErrPreparation` without launching or removing anything.
Rounds and loops ([decision](../decisions/loops-and-review-gate.md)): attempt bindings, the `previous` and `feedback` ports, blocking results, `max` with the `loop-exit` decision, stall messages, `ErrOutcome` for a stale or undispositioned outcome and `ErrReviewGate` before `deliver`.
Its consumer seams: `Records` (the store methods it calls), `Prompter` with `PromptFunc`, `Outbox` (the send half of `messaging.Mailbox`) and the launch seam ([scripted mock agent](../mock-agent.md#the-launch-seam)): `Launcher`, `Spec` and its validation, `Handle`, and the `CheckAdapter` guard that refuses every adapter but `mock` and `fake` inside a test binary.
`BlockedQuestion` is the provisional file a blocked attempt writes.
The private history view ([history](../interpreter.md#history)) is a contract inside the package, not a seam: one parser edge for record names and the reads every step makes of rounds, exits, firings, feedback and outputs.

## Tests

`engine_test.go` (new, red first against a stub `Advance`): the straight workflow against the mock with the in-memory records and messaging fake, and against the mock with a real store file and its mailbox; the approval bound to the plan document; the spec gate; crash between record and launch; the zero-model guard; a failed attempt; mismatched events; a changed revision.
`loops_test.go` (new, red first against the straight-path engine): the §7.1 round trace with three reviews on both backends, stale outcome, missing disposition, stall signals, loop exit, Guildmaster loop, no rule and the review gate.
`prepare_test.go` (new, red first against the render-again replay): a lost launch acknowledgement with a changed baseline, a successful launch whose mark was lost, a changed skill tree, a refused live duplicate leaving every byte and path intact, partial and corrupt preparation refused with `ErrPreparation`, and a leftover `prompt.partial` rendered again.
Its integration case launches the test binary as a child that reads its copied input after the replay and asserts the bytes it read are the original ones.
`TestCrashBetweenRecordAndLaunchReplays`, the straight-path and loop tests and the renderer's digest goldens pin the existing replay and rendering behaviour unchanged.
`history_test.go` (new, red first against the engine before the view existed): the name parser, exit ordering with interleaved rounds and exits, numeric step suffixes, missing and corrupt originating history and reads after each write; the existing exit-ordering test moved here unchanged as a pin.
`TestRoundTraceThreeReviews` also runs with step ids `review`, `review-2` and `review-x2`; that variant pins behaviour the old parser already had.
`TestReplayAfterEachWrite` (pin, written with the view; the engine passed it before): every write of a continued and accepted loop failed in turn, before and after it commits, then the advance repeated.
`TestRecordsContract` runs the shared `Records` suite against `*store.Store` and `interptest.Records`.
`launcher_test.go` (the guard inside and outside a test binary); `Spec.Validate` through the shared suite in `interptest`.
