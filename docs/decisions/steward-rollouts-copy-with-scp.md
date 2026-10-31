---
status: accepted
date: 2026-10-20
topic: steward
amended-by: optional-steward.md, release-upgrades.md
formerly: D12, 0012-steward-rollouts-copy-files-with-scp.md
---

# Steward rollouts copy files with scp and run no remote command

## Context

A Steward rollout publishes files to Director machines that may run POSIX or Windows OpenSSH servers, from roster values the Steward does not fully trust.

## Decision

Rollouts in `internal/steward` run only `scp -q -B -o BatchMode=yes -- <source> <destination>` from `PATH`, as an argument list, and no remote command.
Roster aliases and copy directories are restricted to characters no remote shell or `scp` interprets.
See [apply](../steward.md#apply) for revision preservation, successful-upload verification and failed-transfer limitations.

## Alternatives rejected

- A remote shell or remote commands: shell quoting differs between POSIX and Windows OpenSSH servers, and untrusted roster values could inject options or commands.

## Consequences

The user's SSH config, agent, keys and host-key checking apply unchanged, and the rollout works the same against both server kinds.
Atomic replacement would need a remote rename command, so publication copies the file in place.
The designed [optional Steward](optional-steward.md) and [release upgrades](release-upgrades.md) each allow one fixed remote command (`guildhall config reload` and `guildhall director reload`) once built.
