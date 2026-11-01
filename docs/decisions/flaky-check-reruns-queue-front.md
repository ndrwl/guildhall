---
status: accepted
date: 2026-10-31
topic: ci
tasks: d143
amends: flaky-check-reruns.md
---

# The rebased merge-queue front re-runs a failed CI job once, as validate does

## Context

[Validate's reruns](flaky-check-reruns.md) left out the checks of a rebased merge-queue front, read by deliver's `headChecks`: a flaky check there still escalated back to build, costing a build round and the task's place in the queue.
Guildmaster took the recommendation of d112's open question 1: extend the reruns to the front, reusing d112's rerun record.

## Decision

The front's read of a rebased head's checks follows validate's rule, records, messages and `flaky_checks` policy unchanged ([reruns](../interpreter.md#validate-reruns)).
There is one rerun per step, rebased head and check; the records are `deliver-rerun-…`, keyed on the step, so they never collide with validate's for the same head.
While the matrix finishes and the rerun runs, the front records nothing, sends `checks-pending` with the rebased head's wording and keeps its place.
A second failure, a refused rerun, or a failing check that is not an Actions job escalates back to build as red checks do today, through the unchanged rebase-escalated path; the escalation reason names the checks that failed again after one rerun.
A pass after a rerun is a flake, recorded in the checks result and, under `rerun-and-file`, sent as a `flake` message, again on a replay after a crash, before the front merges.

## Alternatives rejected

- No reruns while holding the front, escalating on the first red check as before: it keeps the queue moving, but a flake costs a build round, a new merge decision and the task's place in the queue, far more than the wait.

## Consequences

The front is held longer: through the rest of the matrix and the rerun's run, a later task waits behind it, and the base has more time to move; a moved base is only noticed after the checks finish, as today.
No record schema changes: the checks result's `reruns` and `flaky_checks` are omitted when empty, so existing records and digests are unchanged, as are the workflows.
