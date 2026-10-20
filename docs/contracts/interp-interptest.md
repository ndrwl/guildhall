# `internal/interp/interptest`

## Contract

`Script`, the scripted mock agent: a `Launcher` double that matches each launch against the next expectation, fails the test on any mismatch and writes scripted responses atomically ([scripted mock agent](../mock-agent.md)).
`RunLauncherContract` and its `Harness`, the shared `Launcher` suite; `Attempt` and the fixture helpers over `internal/interp/testdata`.
`Records`, the in-memory `interp.Records` double, and `RunRecordsContract` with its `RecordsHarness`, the shared suite it passes with the store ([interpreter](../interpreter.md#seams)).

## Tests

`script_test.go`: the shared suite against `Script`; every failure mode against a recording `TB`; responses against golden result, review and question files; golden fixture manifests; unlaunched expectations; an existing result never replaced; concurrent launches.
The launcher suite does not yet run against a real launcher ([outstanding](../mock-agent.md#outstanding)).
The records suite runs against the store and the double in `internal/interp/engine_test.go`; the double was written with its suite, so its passing is not red-first evidence.
