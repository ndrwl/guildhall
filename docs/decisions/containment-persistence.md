---
status: accepted
date: 2026-10-21
topic: containment
formerly: D37, 0037-containment-persistence.md
---

# Containment persists as a recovery description, not a name

## Context

The store's [ownership record](store-attempt-ownership.md) required every containment unit to have a name, which a process group does not have.
The Linux fallback and macOS backends could only be recorded with an invented name, such as the group id, which is reused once its leader is gone.

## Decision

The ownership record describes the owner's containment unit with the same fields as Artisan's `Containment` (kind, name, Linux detection reason) plus the worker identity, so every backend's description is stored as it is reported ([containment record](../store.md#containment-record)).
A process group is found by its leader, so its record carries the worker's PID and creation token instead and must not carry a name; a job or cgroup keeps its name.
The store owns the record's kind vocabulary as plain strings equal to Artisan's kinds, and a composition test in `internal/artisan` stores each backend's description, so the two cannot drift silently.
`Containment.Recoverable` checks structural recovery evidence, not whether a unit is empty; a record that fails it, including an older incomplete one, is still readable but never authorises a sweep or a take-over.
Unknown identity or surviving contained processes never grants take-over.

## Alternatives rejected

- An invented name for a process group, such as its group id: the id is reused once the leader is gone.
- Granting take-over by probing only the launcher: it cannot rule out surviving workers or descendants.

## Consequences

The [attempt ownership contract](../store.md#attempt-ownership) defines explicit probing, the containment-aware recovery path and monotonic recovery evidence, because `TakeOwnership`'s refusal only protects evidence still in the record.
`TestTakeOwnershipPreservesContainmentAfterLauncherExit` in `internal/store/process_test.go` pins the refusal without discarding recovery evidence; the [store contract tests](../contracts/store.md#tests) record the regression coverage, the dependency direction and explicit probing.
The store-neutral identity and probe values remove the store's dependency on Artisan without changing persisted records.
