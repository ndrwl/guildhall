---
status: accepted
date: 2026-10-21
topic: engine
amended-by: cli-role-grammar.md
formerly: D45, 0045-launch-composition.md
---

# `internal/launch` is the only place the owned attempt commands are composed

## Context

The owned attempt commands, now `artisan start`, `stop` and `status` ([role grammar](cli-role-grammar.md)), need the adapter, the artisan launcher, the store and reconcile together.
Composing them in the command line, or letting `artisan` import the adapter, would spread that wiring across packages.

## Decision

- `internal/launch` is the one composition package: it alone imports the adapter, artisan, store and reconcile together.
  `artisan` stays free of the adapter, and the command line only parses flags and prints JSON.
- The launcher opens the Director store for each record operation and closes it before the next, so a running attempt never holds the store's single-writer lock and other commands work meanwhile.
  Ownership is a store record, not a held handle.
- Custody is proven, never assumed: a start takes ownership with a containment-aware probe, so only a crashed launcher whose recorded unit is proven empty is replaced, and missing containment prevents takeover even after the launcher exits.
  Survivors after the stop ladder's last rung leave the attempt running and owned, because finishing it would release the record a later sweep needs.
  `stop` asks the owner through its endpoint and otherwise requires verified termination; a classification alone is not proof.

Takeover, stop and exit codes in detail are behaviour, defined under [Artisan commands](../artisan.md#commands) and the [launch contract](../contracts/launch.md).

## Consequences

Every caller that launches an owned attempt, the command line and the interpreter's `launch.Process`, goes through one package, so the custody rules live in one place.
The direct-child mode of `artisan run` was replaced by the owned mode; the Herdr tab mode remains ([running an attempt](../adapters.md#running-an-attempt)).
The [resume decision](artisan-resume.md) owns the later recovery scope.
