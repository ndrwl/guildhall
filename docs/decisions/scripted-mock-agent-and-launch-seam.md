---
status: accepted
date: 2026-10-21
topic: engine
formerly: D27, 0027-scripted-mock-agent-and-launch-seam.md
---

# The interpreter owns its launch seam, and one scripted mock agent stands in for workers in tests

## Context

The interpreter's tests must run workflows without a real agent or adapter, and must not claim more about agent behaviour than they check ([scripted mock agent](../mock-agent.md)).

## Decision

- The interpreter declares its own narrow launch interface, `interp.Launcher`, rather than consuming the adapter package directly, because it is the seam the interpreter's tests replace and it carries the rendered manifest the adapters never see.
- `interp.Spec` names the adapter explicitly, so a launcher can refuse an adapter it does not run and the mock can refuse anything but itself.
- The mock agent is one scripted double in `interptest`: it checks what it is given against an ordered script, returns scripted responses, and claims nothing about semantic behaviour.
- The no-real-adapter guard keys on `testing.Testing()`, which every Go test binary sets.
- The [script refusal semantics](../mock-agent.md#scripts) keep one matching mistake from being reported repeatedly.

## Alternatives rejected

- Consuming the adapter package directly: the interpreter's tests could not replace it, and it never sees the rendered manifest.
- A separate mock framework: one scripted double is enough for what the tests check.
- A flag to enable the guard: a CI configuration could forget it.

## Consequences

The blocked question file was provisional and owned by `internal/interp` until result intake defined how the interpreter reads it.
