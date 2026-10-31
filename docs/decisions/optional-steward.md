---
status: designed
date: 2026-10-21
topic: steward
amends: steward-rollouts-copy-with-scp.md
formerly: D50, 0050-optional-steward.md
---

# A Director runs standalone or managed, and no Director feature requires a Steward

## Context

A Guildmaster with one machine should not need a Steward, while a managed Director must not trust configuration that merely appeared on disk.
Designed, not built ([director modes](../director-modes.md)).

## Decision

A Director runs standalone or managed, and no Director feature may require a Steward.
The mode is a store fact recorded by explicit `enrol` and `leave` commands, not a config key.
Legacy inline `[managed]` settings are refused with guidance to move them to checksum-bearing `managed.toml`, and a copy with that file but no enrolment record must explicitly enrol.
Guard rails are enforced by the binary in both modes: standalone takes them from local config; managed lets `managed.toml` win for managed keys, which the Director never writes.
Leaving keeps the last managed values as local values, so leaving never loosens a guard rail silently.
The Steward acts by command: rollout copies stay `scp`, followed by `guildhall config reload` over SSH to validate and record authorization for the rollout digest in the receiving store.
A digest with no receiving-store rollout, reload or explicit Guildmaster approval is recorded as `observed-untrusted`, raises a system alert and is not applied; a checksum, enrolment, or a receipt held only on the Steward does not authorize it.
Active tasks retain their bound configuration, and commands needing effective settings refuse an untrusted on-disk revision.
Lessons are recorded locally in both modes and collected by the Steward on request when managed, so the Director never reaches the Steward or the private repository (R1.8).

## Alternatives rejected

- A `mode` config key: the key `mode` already means a copy's role (R1.9), and a store event gives the change a receipt and a place in the timeline.
- A `managed (unrecorded)` compatibility state for copies with `managed.toml` but no enrolment: Guildhall is pre-release, so it keeps no compatibility state.

## Consequences

`guildhall config reload` is a remote command, so adopting it amends [scp rollouts](steward-rollouts-copy-with-scp.md)' "no remote command" for that one fixed command.
Director suites that load config run on a standalone and a managed copy with the same effective settings, so the rule that no feature needs a Steward is tested rather than assumed.
