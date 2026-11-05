---
status: accepted
date: 2026-10-20
topic: steward
tasks: d216
formerly: D14, 0014-separate-managed-publication-from-director-owned.md
---

# Rollouts write only `managed.toml`, separate from the Director-owned `config.toml`

## Context

A Director edits its own `config.toml` locally, so a rollout that replaced the whole file from a Steward snapshot could overwrite a concurrent local edit with stale content.

## Decision

Rollouts write only `managed.toml`, never `config.toml`.
A checksum framing line detects incomplete managed transfers.
A kernel lock on `.rollout.lock` in the private config directory prevents concurrent local applies sharing that directory; the OS releases it when its owner dies, and the file stays only to hold the owner's record.
It was chosen over manual recovery of a lock file so that a crash during a 30-minute takeover apply no longer stops every scheduled collection until someone removes the file.
See [configuration](../configuration.md) for combined loading and legacy inline-layout compatibility, and [Steward revisions and active tasks](../steward.md#revisions-and-active-tasks) for revision retention and task binding.

## Consequences

scp publication is non-atomic ([scp rollouts](steward-rollouts-copy-with-scp.md)) and may transiently refuse reads, so this does not establish uninterrupted config availability.
The lock does not cover cross-host writers: operators must designate one writer.
A crashed writer leaves no stale lock to recover; a refusal names a live owner, possibly a hung one, which the Guildmaster stops.
The file must never be deleted, and every Steward command on a machine must run the same `guildhall` version, since an older binary's lock is the file's existence.

## History

- 2026-11-05, d216: replaced the exclusive lock file and its manual stale-lock recovery with the kernel lock, as d209's hallmark report Q1 recommended and the Guildmaster accepted.
