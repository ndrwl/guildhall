---
status: accepted
date: 2026-10-20
topic: store
formerly: D24, 0024-inspection-reads-from-store-receipts.md
---

# Inspection reads take their times from store receipts

## Context

The Director's inspection API starts with reads over the store: `task timeline` and `task files` (see [inspection](../tasks.md#inspection)).
They need times and durations, and records carry no timestamps of their own.

## Decision

- Times and durations come from receipts, because every mutation already commits a receipt with a sequence and a commit time in the same transaction as its records.
- To attribute a receipt to an attempt, decision or document, receipts gain an optional record name outside the request digest, so existing receipts replay unchanged, decode with no record, and the store stays at schema 1.
- A read is labelled with the newest committed sequence and the task's workflow revision rather than the time it ran, so two reads of the same store state print the same output and a reader can tell whether anything changed between them.
- A task's receipts are found by scanning every receipt.

## Alternatives rejected

- New timestamp fields on records: receipts already hold the commit time.
- A second index of receipts by task: it would change the file layout; one can be added if a Director's store grows large enough to need it.

## Consequences

A read's cost grows with the whole store's receipts, not the task's.
Diff, process and transcript reads need worktree custody and the adapter, and were left to that work.
