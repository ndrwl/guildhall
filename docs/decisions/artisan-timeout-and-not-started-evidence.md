---
status: accepted
date: 2026-10-26
topic: containment
formerly: ci-timing-flakes.md
---

# An owned Artisan's timeout starts after setup, and a launcher that never started a worker records `not-started` evidence

## Context

CI timing flakes on 2026-10-26 exposed two product races ([regression evidence](../evidence/ci-timing-flakes-2026-10-26.md)).
An owned Artisan's `--timeout` could expire while taking ownership, so a slow runner failed `start` before any worker ran.
A stop after a deliberately cancelled pre-start launch could not prove the attempt stopped when no named containment had been recorded.
The test-side half of the change, that tests synchronize on events rather than runner time and that a guard enforces generous ceilings, is the [test timing policy](../test-timing.md).

## Decision

An owned Artisan's `--timeout` is a result-wait budget, beginning only after the worker starts and its ownership is recorded.
It must not expire while taking ownership, binding the endpoint or starting the worker; caller cancellation remains effective throughout setup.
An ownership wait checks cancellation before accepting a ready endpoint, so delayed reads cannot turn an expired wait into success.

A launcher which finishes without ever asking its unit to start a worker records explicit `not-started` containment evidence when no named containment was recorded.
That evidence is published only after supervision ends, never in the crash window before startup.
Missing containment, a failed startup invocation and a crashed launcher remain conservative, not equivalent to `not-started`.

See [Artisan commands](../artisan.md#commands), the [launch contract](../contracts/launch.md) and the [store containment record](../store.md#containment-record) for the behaviour.

## Consequences

A later stop can prove termination after a deliberately cancelled pre-start launch on process-group platforms without weakening crash recovery.
`not-started` proves there are no worker processes, but does not prove the launcher itself has exited.
A slow setup makes `start` slow, not failed, while cancellation still stops it promptly.
