---
status: accepted
date: 2026-10-21
topic: containment
formerly: D31, 0031-store-attempt-ownership.md
---

# Attempt ownership lives in the store

## Context

The scratch launcher recorded which launcher owns a stage attempt in an `owner.json` file guarded by its own kernel file lock.
The store already serializes writers across processes under its [single-writer lock](store-single-writer-lock.md) and makes every write durable with a receipt.

## Decision

Ownership is a store record written under the store's single-writer lock, replacing `owner.json` and its lock ([attempt ownership](../store.md#attempt-ownership)).
A check-and-set inside one mutation gives the same exclusion as the scratch lock, plus replay by mutation id and an audit trail.
The store does not interpret liveness itself: `TakeOwnership` takes an `OwnerProbe`, which the caller must supply.
The launcher composition passes `ProbeLauncher` over Artisan's native identity probe, so the launcher can fold contained survivors into the answer and the tests can script live, unknown and exited owners deterministically.
The probe runs inside the write transaction, which is what makes two concurrent take-overs unable to both succeed.
`UpdateOwnership` sits beside the take so the owner can record its containment unit, endpoint and `running` once they exist, since the endpoint is bound only after ownership is taken.
Ownership records use `TaskID` like every other store record.

## Alternatives rejected

- Keeping `owner.json` and its file lock: a second lock, without replay or an audit trail.
- Letting the store judge liveness: the launcher could not count contained survivors, and tests could not script owners.
- Probing outside the write transaction: two concurrent take-overs could both succeed.
- A `Task` field name, as first planned: every other store record says `TaskID`.

## Consequences

The probe, a bounded local process lookup, runs while the store's writer lock is held.
The launcher mutation-id convention and its replay constraints are documented under [attempt ownership](../store.md#attempt-ownership).
How the containment unit is described in the record is [containment persistence](containment-persistence.md).
