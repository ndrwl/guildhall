# One package owns the runtime names

The choice of runtime was spelled in three places across two processes: `launch.Process` held a list of the adapters it would launch, `artisan start` a map from name to adapter, and its Herdr tab path built the fake directly; the conformance suite's spec mapping, `interp.Spec.AdapterSpec`, was not the one the launcher sent (sustainability review 9, F28).
Every new runtime would have had to edit each site, and a drift between the suite's spec and the launcher's arguments was caught only end to end.

Package `internal/adapter/runtimes` is now the one owner: `Lookup` maps a name to its runtime over the guildhall executable, `Names` lists them, and its constants `Fake` and `Claude` are the names' one spelling, which `interp.FakeAdapter` and `interp.ClaudeAdapter` refer to.
It is a subpackage beside the adapter contract rather than package `adapter` itself, because the runtimes import `adapter` and package `adapter` cannot import them back; the [layout rules](../contracts.md#layout) already allow runtime implementations in subpackages.
`artisan start` consults it for `--adapter` in both modes and refuses an unknown name with the list.
`launch.Process` no longer refuses names: it passes the engine's name through and reports the child's refusal as it reports every other startup failure, with the child's log, so the launcher's list cannot fall behind.
`launch.Process` builds the child's prompt and working directory from `interp.Spec.AdapterSpec` and checks its identity and result file against the attempt layout, so the suite's mapping is the launcher's.
The interpreter's test-binary guard, `interp.CheckAdapter`, stays in the interpreter: it guards what a test may launch, not what Guildhall can.
