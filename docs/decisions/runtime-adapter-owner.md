---
status: accepted
date: 2026-10-22
topic: runtimes
formerly: D58, 0058-runtime-adapter-owner.md
---

# One package owns the runtime names

## Context

The choice of runtime was spelled in three places across two processes: `launch.Process` held a list of the adapters it would launch, `artisan start` a map from name to adapter, and its Herdr tab path built the fake directly; the conformance suite's spec mapping, `interp.Spec.AdapterSpec`, was not the one the launcher sent (sustainability review 9, F28).
Every new runtime would have had to edit each site, and a drift between the suite's spec and the launcher's arguments was caught only end to end.

## Decision

Package `internal/adapter/runtimes` is the one owner: `Lookup` maps a name to its runtime over the guildhall executable, and `Names` lists them.
The names themselves are spelled once, as `adapter.FakeName` and `adapter.ClaudeName`: the lookup is keyed by them and `interp.FakeAdapter` and `interp.ClaudeAdapter` refer to them.
`artisan start` consults it for `--adapter` in both modes and refuses an unknown name with the list.
`launch.Process` no longer refuses names: it passes the engine's name through and reports the child's refusal as it reports every other startup failure, with the child's log.
`launch.Process` builds the child's prompt and working directory from `interp.Spec.AdapterSpec` and checks its identity and result file against the attempt layout, so the suite's mapping is the launcher's.
The interpreter's test-binary guard, `interp.CheckAdapter`, stays in the interpreter: it guards what a test may launch, not what Guildhall can.

## Alternatives rejected

- The names in package `runtimes`: `runtimes` imports the fake and Claude adapters, whose tests import the interpreter, so an interpreter import of `runtimes` would be a cycle.
- The lookup in package `adapter` itself: the runtimes import `adapter`, which cannot import them back; the [layout rules](../contracts.md#layout) already allow runtime implementations in subpackages.
- Keeping a list in `launch.Process`: it could fall behind `artisan start`'s.

## Consequences

A new runtime is added in one package and its name in one place.
A launch of an unknown runtime now fails in the child rather than in the launcher, so the refusal arrives through the child's log.
