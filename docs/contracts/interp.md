# `internal/interp`

## Contract

`Engine.Advance`, the deterministic interpreter ([interpreter](../interpreter.md)): the events, what each records, the spec gate, derived mutation ids and replay after a crash between recording and launching an attempt.
Attempt preparation ([interpreter](../interpreter.md#attempt-preparation), [decision](../decisions/0036-immutable-attempt-preparation.md)): a prepared attempt is published once by renaming `prompt.partial` to `prompt`, replays launch from it unchanged after validating it against its manifest and recorded binding, and invalid preparation is refused with `ErrPreparation` without launching or removing anything.
Rebase-aware review ([interpreter](../interpreter.md#rebase-aware-review)) defines `Engine.Base`, the `Rebases` seam, the binding's `range` and `scope`, port `resolution`, and the revision-equality prerequisite for narrowing, and the full-review request a round consumes.
Rounds and loops ([decision](../decisions/0035-loops-and-review-gate.md)): attempt bindings, the `previous`, `rulings` and `feedback` ports, blocking results, `max` with the `loop-exit` decision, stall messages, contested Director rulings and the dispute loop exit ([decision](../decisions/0040-director-rulings.md)), the review projection on outcome documents, `ErrOutcome` for a stale or undispositioned outcome and `ErrReviewGate` before `deliver`.
Its consumer seams: `Records` (the store methods it calls), `Prompter` with `PromptFunc`, `Outbox` (the send half of `messaging.Mailbox`) and the launch seam ([scripted mock agent](../mock-agent.md#the-launch-seam)): `Launcher`, `Spec` with its validation and `AdapterSpec`, the reference `adapter.Spec` mapping the adapter conformance suite uses (production launch builds its own spec today), `Handle`, and the `CheckAdapter` guard that refuses every adapter but `mock`, `fake` and a `claude` that resolves on `PATH` to the stub `ClaudeStubEnv` names inside a test binary.
`BlockedQuestion` is the provisional file a blocked attempt writes.
The private history view ([history](../interpreter.md#history)) is a contract inside the package, not a seam: one parser edge for record names and the reads every step makes of rounds, exits, firings, feedback and outputs.

The [replay and recovery contract](../interpreter.md#replay-and-recovery) owns intake completion and concurrent outcome reconciliation.

The [worker-stalled observation contract](../interpreter.md#worker-stalled-observations) defines the heartbeat file read, configured interval and clock, threshold crossings, replay identity and unchanged attempt state.

## Tests

`engine_test.go` (new, red first against a stub `Advance`): the straight workflow against the mock with the in-memory records and messaging fake, and against the mock with a real store file and its mailbox; the approval bound to the plan document; the spec gate; crash between record and launch; the zero-model guard; a failed attempt; mismatched events; a changed revision.
`loops_test.go` (new, red first against the straight-path engine): the §7.1 round trace with three reviews on both backends, stale outcome, missing disposition, stall signals, loop exit, Guildmaster loop, no rule and the review gate.
`prepare_test.go` (new, red first against the render-again replay): a lost launch acknowledgement with a changed baseline, a successful launch whose mark was lost, a changed skill tree, a refused live duplicate leaving every byte and path intact, partial and corrupt preparation refused with `ErrPreparation`, and a leftover `prompt.partial` rendered again.
Its integration case launches the test binary as a child that reads its copied input after the replay and asserts the bytes it read are the original ones.
`TestCrashBetweenRecordAndLaunchReplays`, the straight-path and loop tests and the renderer's digest goldens pin the existing replay and rendering behaviour unchanged.
`rulings_test.go` (new, red first against the engine before rulings existed): on both backends, review 1's outcome projects its finding, the Director's ruling reaches review 2 on port `rulings`, a withdrawn finding leaves it uncontested, and a finding kept open contests it once, even on a replayed advance, and opens loop exit `review-x2` within `max`, quoting the finding, the ruling and the head; `continue` takes the loop.
`history_test.go` (new, red first against the engine before the view existed): the name parser, exit ordering with interleaved rounds and exits, numeric step suffixes, missing and corrupt originating history and reads after each write; the existing exit-ordering assertions are preserved, adapted to the history view.
`TestRoundTraceThreeReviews` also runs with step ids `review`, `review-2` and `review-x2`; that variant pins behaviour the old parser already had.
`history_replay_test.go`: `TestReplayAfterEachWrite` fails each record write of a loop that continues once and is accepted at its second exit, before and after the write commits, against `interptest.Records`; against the real store it fails only the first write of each category.
The flow requests a full review before review 2 and checks that exactly one request remains consumed by `review-2` with round scope `full`, including after failures before and after consumption commits.
`TestReplaySampleCoversEveryWriteKind` checks that the executed flow includes every category in `writeKinds` in `history_replay_test.go`, including full-review consumption.
Store durability remains covered by the [store contract tests](store.md#tests).
Repeating the advance must end with the records, launch count and wait of a run without a failure.
`TestRecordsContract` runs the shared `Records` suite against `*store.Store` and `interptest.Records`; its rulings case (listing, contesting once, an unknown ruling) was written with the double and passed on its first run against the store, which already implemented it.
`worker_stalled_test.go` was first written against the store heartbeat; its earlier post-implementation failure check is in [captured runs](../evidence/heartbeat-2026-10-21.md).
When the heartbeat moved to the attempt directory it was rewritten red first to write `heartbeat.json` with `artisan.WriteHeartbeat`, and failed with `stalls = 0, want 1` until the engine read the file.
It covers both records backends, missing and stale heartbeats, crossings, replay, unchanged attempts, a heartbeat naming another attempt and a newer schema failing the advance.
Review-fix regression tests cover immutable binding replay, acceptance projection and oversized dispute questions.
`rebase_test.go`, on both backends with `gittest.Script`: a rebase gives review 2 the resolution diff and `scope: rebase`; a full classification, no `Base` and a failing `Base` are full, the last two without classifying; a changed standards revision also retains full scope without classification despite an otherwise eligible rebase; review 2's outcome binds to its current head, accepted-spec digest and standards revision in every case.
Its `requested full` case, red first against stubbed full-review records, requests a full review before review 2 of an otherwise rebase-only update: review 2 is full without classifying, records base 2 and head 2, and consumes the request.
The equal second finish of `RunRecordsContract`'s attempts case, returning the first finish's receipt, was red first against the store and the double (the red run is cited in [store](store.md)); it replaced per-writer tolerance in the intake and in `launch.Run`, which `TestLauncherRecordedOutcome`, `TestInterpreterFinishRace` and `launch`'s `TestRunAfterTheInterpreterTookTheResult` pin.
The full-review case of `RunRecordsContract` (refusals, replay, one pending request, consuming once, numbering) was red first against stubs in the store and the double.
The revision-change regression was added during review after implementation.
It came with the implementation from an earlier branch; on 2026-10-21, with `rebase` stubbed to return a full scope and no range, the rebase cases failed (`manifest lacks required input "resolution"`) and so did the full cases' range check.
`launcher_outcome_test.go` was written red first on both backends: a plan the launcher recorded succeeded before its intake, or before any event named it, failed with `plan.spec has no recorded document`, and now has its output recorded once and opens the approval; a stopped outcome standing over a succeeded result passed unchanged and pins that the launcher's outcome stands.
Review regressions exercise interrupted second-output intake and same/conflicting finish races on both backends in `launcher_outcome_test.go`.
The contested case in `rulings_test.go` interrupts intake after the outcome document but before contesting; `TestStallSignals` interrupts notification after the launcher has recorded success.
Recovery must retain every output, contest once, pause for the Guildmaster and deliver the stall notification.
These cases execute `Engine.Advance`.
See the [authorized retrospective comparison](../evidence/s7-review-retrospective.md) for the evidence and its limitations.
`launcher_test.go` (the guard inside and outside a test binary, and `claude` admitted only over the named stub, never a relative name or a real `claude` ahead of it); `adapter_spec_test.go` (new, red first against a stub returning an empty spec: the build and review fixtures map to their identity and paths with no environment, and the mapped spec validates); `Spec.Validate` through the shared suite in `interptest`.
