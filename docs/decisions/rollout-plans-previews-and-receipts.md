---
status: accepted
date: 2026-10-20
topic: steward
formerly: D13, 0013-rollout-plans-previews-and-receipts-live.md
---

# Rollout plans, previews and receipts live in one plan directory

## Context

A Steward rollout needs a reviewable dry run, an approval bound to exactly what was previewed, and a record of what was applied.

## Decision

A dry run writes `plan.json`, the per-copy previews and, after apply, timestamped receipts into one new directory named by `--plan`.
The plan digest is the sha256 of the plan's JSON encoding with its digest field set to an empty string.
The managed-settings source marks policy keys with `policy = true`, which is kept privately and not written to copies.
See [apply](../steward.md#apply) for revalidation, machine selection, temporary transport inputs and receipt contents.

## Consequences

Receipts are the rollout record for now; they are not a task store.
Only the Guildmaster can say which preferences are review or approval rules, so policy marking stays in the Steward's private source.
