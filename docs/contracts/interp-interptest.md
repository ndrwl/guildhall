# `internal/interp/interptest`

## Contract

`Script`, the scripted mock agent: a `Launcher` double that matches each launch against the next expectation, fails the test on any mismatch and writes scripted responses atomically ([scripted mock agent](../mock-agent.md)).
`RunLauncherContract` and its `Harness`, the shared `Launcher` suite; `Attempt` and the fixture helpers over `internal/interp/testdata`.
`Records`, the in-memory `interp.Records` double (with `RequestFullReview`, the Director's write the engine never makes), and `RunRecordsContract` with its `RecordsHarness`, the shared suite it passes with the store ([interpreter](../interpreter.md#seams)).

## Tests

`script_test.go`: the shared suite against `Script`; every failure mode against a recording `TB`; responses against golden result, review and question files; golden fixture manifests; unlaunched expectations; an existing result never replaced; concurrent launches.
The launcher suite also runs against `launch.Process` in `internal/launch/process_test.go`; its stop case was widened to accept `adapter.ErrAttemptCancelled` from the wait, because the real launcher's cancel marker ([adapters](../adapters.md)) makes the wait refuse a stopped attempt, and now also asserts no result file exists ([against the real launcher](../mock-agent.md#against-the-real-launcher)).
The records suite runs against the store and the double in `internal/interp/engine_test.go`; the double was written with its suite, so its passing is not red-first evidence.
