# `internal/interp`

## Contract

`Engine.Advance`, the deterministic interpreter ([interpreter](../interpreter.md)): the events, what each records, the spec gate, derived mutation ids and replay after a crash between recording and launching an attempt.
Attempt preparation ([interpreter](../interpreter.md#attempt-preparation), [decision](../decisions/immutable-attempt-preparation.md)): a prepared attempt is published once by renaming `prompt.partial` to `prompt`, replays launch from it unchanged after validating it against its manifest and recorded binding, and invalid preparation is refused with `ErrPreparation` without launching or removing anything.
Rounds and loops ([decision](../decisions/loops-and-review-gate.md)): attempt bindings, the `previous`, `rulings` and `feedback` ports, blocking results, `max` with the `loop-exit` decision, stall messages, contested Director rulings and the dispute loop exit ([decision](../decisions/director-rulings.md)), the review projection on outcome documents, `ErrOutcome` for a stale or undispositioned outcome and `ErrReviewGate` before `deliver`.
Its consumer seams: `Records` (the store methods it calls), `Prompter` with `PromptFunc`, `Outbox` (the send half of `messaging.Mailbox`) and the launch seam ([scripted mock agent](../mock-agent.md#the-launch-seam)): `Launcher`, `Spec` and its validation, `Handle`, and the `CheckAdapter` guard that refuses every adapter but `mock` and `fake` inside a test binary.
`BlockedQuestion` is the provisional file a blocked attempt writes.
The private history view ([history](../interpreter.md#history)) is a contract inside the package, not a seam: one parser edge for record names and the reads every step makes of rounds, exits, firings, feedback and outputs.

The [worker-stalled observation contract](../interpreter.md#worker-stalled-observations) defines the heartbeat read seam, configured interval and clock, threshold crossings, replay identity and unchanged attempt state.

## Tests

`engine_test.go` (new, red first against a stub `Advance`): the straight workflow against the mock with the in-memory records and messaging fake, and against the mock with a real store file and its mailbox; the approval bound to the plan document; the spec gate; crash between record and launch; the zero-model guard; a failed attempt; mismatched events; a changed revision.
`loops_test.go` (new, red first against the straight-path engine): the §7.1 round trace with three reviews on both backends, stale outcome, missing disposition, stall signals, loop exit, Guildmaster loop, no rule and the review gate.
`prepare_test.go` (new, red first against the render-again replay): a lost launch acknowledgement with a changed baseline, a successful launch whose mark was lost, a changed skill tree, a refused live duplicate leaving every byte and path intact, partial and corrupt preparation refused with `ErrPreparation`, and a leftover `prompt.partial` rendered again.
Its integration case launches the test binary as a child that reads its copied input after the replay and asserts the bytes it read are the original ones.
`TestCrashBetweenRecordAndLaunchReplays`, the straight-path and loop tests and the renderer's digest goldens pin the existing replay and rendering behaviour unchanged.
`rulings_test.go` (new, red first against the engine before rulings existed): on both backends, review 1's outcome projects its finding, the Director's ruling reaches review 2 on port `rulings`, a withdrawn finding leaves it uncontested, and a finding kept open contests it once, even on a replayed advance, and opens loop exit `review-x2` within `max`, quoting the finding, the ruling and the head; `continue` takes the loop.
`history_test.go` (new, red first against the engine before the view existed): the name parser, exit ordering with interleaved rounds and exits, numeric step suffixes, missing and corrupt originating history and reads after each write; the existing exit-ordering assertions are preserved, adapted to the history view.
`TestRoundTraceThreeReviews` also runs with step ids `review`, `review-2` and `review-x2`; that variant pins behaviour the old parser already had.
`history_replay_test.go`: `TestReplayAfterEachWrite` (pin, written with the view; the engine passed it before) fails each record write of a loop that continues once and is accepted at its second exit, before and after the write commits, on both backends.
Repeating the advance must end with the records, launch count and wait of a run without a failure.
`TestRecordsContract` runs the shared `Records` suite against `*store.Store` and `interptest.Records`; its rulings case (listing, contesting once, an unknown ruling) was written with the double and passed on its first run against the store, which already implemented it.
`worker_stalled_test.go` and the heartbeat case in `RunRecordsContract` were written before implementation but were not observed failing then.
On 2026-10-21, after implementation was written, both heartbeat reads were temporarily stubbed to return `ErrNotFound`: `TestWorkerStalled/{double,store}` failed with `stalls = 0, want 1`, and `TestRecordsContract/{store,double}/heartbeat` failed because the persisted heartbeat read returned `record not found`.
The stubs were then removed for the focused verification.
[Captured runs](../evidence/heartbeat-2026-10-21.md) document this post-implementation failure check, not historical red-first evidence.
The tests cover both backends, missing/stale heartbeats, crossings, replay and unchanged attempts.
Review-fix regression tests cover immutable binding replay, acceptance projection and oversized dispute questions.
`launcher_test.go` (the guard inside and outside a test binary); `Spec.Validate` through the shared suite in `interptest`.
