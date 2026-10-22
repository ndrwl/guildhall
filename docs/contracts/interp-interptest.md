# `internal/interp/interptest`

## Contract

`Script`, the scripted mock agent: a `Launcher` double that matches each launch against the next expectation, fails the test on any mismatch and writes scripted responses atomically ([scripted mock agent](../mock-agent.md)).
`RunLauncherContract` and its `Harness`, the shared `Launcher` suite; `Attempt` and the fixture helpers over `internal/interp/testdata`.
`AdapterSpec`, the build fixture rendered with given step instructions and mapped through `interp.Spec.AdapterSpec`, for the [adapter conformance suite](adapter-adaptertest.md).
`Records`, the in-memory `interp.Records` double, including `SetTaskState`, (with `RequestFullReview`, the Director's write the engine never makes), and `RunRecordsContract` with its `RecordsHarness`, the shared suite it passes with the store ([interpreter](../interpreter.md#seams)).
The double's `StartCappedAttempt` refuses an unknown or closed task before it counts, as the store does, and counts with `store.CountUsage` by its `Now`, wall time when nil, which also stamps its receipts.
`RecordsHarness.Clock` is the harness's clock, a `*Clock` that stamps the receipts of the harness's `Records` (`Records.Now` for the double, `store.Options.Now` for a store), so a case that sets it decides the day every backend counts.
`RunRecordsContract`'s caps cases run on that clock: one refuses a start at the running cap across two tasks, recording nothing, replays an admitted start under smaller caps, admits after a finish and refuses at the daily count; one refuses the daily cap until the clock crosses a UTC midnight and then admits, naming each refusal's day; one refuses a capped start of a closed task with `store.ErrInvalid`, not a cap refusal.
`RecordsHarness.PauseDirector` pauses the Director, the double's `PauseDirector` or `PauseStoreDirector` for a store; the hold case then refuses a start with `AttemptCaps.Hold` with `store.ErrDirectorHeld`, recording nothing, returns a replayed start's receipt and admits a start without `Hold`.
`RecordsHarness.Writers` (default 4) and `Operations` (default 30) size the concurrent case; an implementation that opens the store file per operation across processes sets writers to the number production has, two, and says so in its harness ([one writer](../store.md#one-writer)).

## Tests

The hold case was written red first against a double and `PauseStoreDirector` that did nothing ([red run](../evidence/director-stop-admission-2026-10-22-red.txt)), failing on the double, the store and `cli.storeRecords`.

`script_test.go`: the shared suite against `Script`; every failure mode against a recording `TB`; responses against golden result, review and question files; golden fixture manifests; unlaunched expectations; an existing result never replaced; concurrent launches.
The launcher suite also runs against `launch.Process` in `internal/launch/process_test.go`; its stop case was widened to accept `adapter.ErrAttemptCancelled` from the wait, because the real launcher's cancel marker ([adapters](../adapters.md)) makes the wait refuse a stopped attempt, and now also asserts no result file exists ([against the real launcher](../mock-agent.md#against-the-real-launcher)).
The records suite runs against the store and the double in `internal/interp/engine_test.go`; the double was written with its suite, so its passing is not red-first evidence.
It also runs against the per-operation adapter in `internal/cli/records_test.go` at two writers; `TestStoreRecordsHarnessNamesWriters` pins that the harness names its writer count.
The caps case was red against the double before it counted ([red run](../evidence/guard-rails-2026-10-22-red.txt)); it passed on its first run against the store and `storeRecords`, which were implemented first.
The midnight and closed-task cases and `TestDailyCapRefusesUntilTheNextDay` over both engine backends were red with the clock wired only into the double ([red run](../evidence/attempt-cap-count-2026-10-22-red.txt)): the store backends counted wall time, and the double answered a closed task at a cap with a cap refusal; the store needed no change beyond counting through `store.CountUsage`.
