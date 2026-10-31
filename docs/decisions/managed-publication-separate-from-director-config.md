---
status: accepted
date: 2026-10-20
topic: steward
formerly: D14, 0014-separate-managed-publication-from-director-owned.md
---

# Rollouts write only `managed.toml`, separate from the Director-owned `config.toml`

## Context

A Director edits its own `config.toml` locally, so a rollout that replaced the whole file from a Steward snapshot could overwrite a concurrent local edit with stale content.

## Decision

Rollouts write only `managed.toml`, never `config.toml`.
A checksum framing line detects incomplete managed transfers.
An exclusive lock file in the private config directory prevents concurrent local applies sharing that directory.
See [configuration](../configuration.md) for combined loading and legacy inline-layout compatibility, and [Steward revisions and active tasks](../steward.md#revisions-and-active-tasks) for revision retention and task binding.

## Consequences

scp publication is non-atomic ([scp rollouts](steward-rollouts-copy-with-scp.md)) and may transiently refuse reads, so this does not establish uninterrupted config availability.
The lock does not cover cross-host writers: operators must designate one writer and recover a stale lock only after confirming its owner has stopped.
