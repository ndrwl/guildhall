---
status: accepted
date: 2026-10-21
topic: containment
formerly: D41, 0041-stop-ladder-precedence.md
---

# The launcher decides each attempt once, from its files before its processes

## Context

A worker's result, a stop request and a later reconcile of a dead launcher's attempt can all race to decide the same attempt.
A delivered signal does not prove that processes ended ([containment](artisan-containment.md)).

## Decision

The launcher decides each attempt once, from its files before its processes ([stop ladder](../artisan.md#stop-ladder)): a cancel marker means cancelled and makes any result late, otherwise a valid result means finished, otherwise a stop writes the marker.
The marker is written under an attempt lock (`attempt.lock`, `flock` on Unix and `LockFileEx` on Windows) after checking again for a result, so the launcher, and later a reconcile, never decide the same attempt two ways.
The worker takes no lock, because a result it publishes after the marker is late whatever order the files land in.
The decision goes to `outcome.json`, published with a hard link so it is never replaced.
The launcher reads the result and writes the marker through the `AttemptFiles` seam; the real one is the composition package's `launch.Files`, over the adapter's identity-bound, version-checked result parse and its `.cancelled` sidecar.
`RunStopContract` runs against it and `reconcile.Classify` consumes the same value, so marker and result have one reader each.
A rung counts only when the unit is empty, never because a signal was delivered.

## Alternatives rejected

- Reading the adapter's result file and `.cancelled` sidecar directly: the ladder could not be tested with a file double.
- A lock taken by the worker too: unnecessary, since a result after the marker is late in any order.

## Consequences

Because an error reading the attempt's files decides it failed, every `AttemptFiles` adapter reads the result through the [bounded retry policy](../contracts/atomicfile.md) before returning an error, and `RunStopContract` checks it on Windows for each adapter.
Windows has no terminate rung, and its interrupt rung is `CTRL_BREAK`, which reaches only a worker sharing the launcher's console and which a worker may treat as fatal.
It is kept as rung 2 but recorded as not a dependable graceful rung; rung 1, the marker, is the graceful stop on every platform.
