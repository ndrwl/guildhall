---
status: accepted
date: 2026-10-26
topic: engine
---

# The spend report shows recorded spend without inventing attribution

## Context

The adoption plan asks for per-role and per-step token attribution before any cost tuning.
The attempt store and versioned usage files ([attempt spend](attempt-spend.md)) record aggregate input, output, cache-creation, cache-read and USD amounts, with independent token and cost provenance.

## Decision

`spend report` groups existing attempts by repository, task, step and available historical metadata.
It gives the store's recorded spend precedence over the attempt usage file, so retries remain separate and a persisted amount is never counted twice.
Measured and estimated amounts have separate subtotals; unknown counts make the report explicitly partial, including running attempts without reported usage.
Historical profile and role source come from the attempt's rendered manifest, not today's mutable configuration.
Report aggregation stays within the CLI package; the store boundary owns locking and immutable record reads, and the adapter boundary owns usage-file validation.
The report opens the store read-only and never creates a missing copy.

## Alternatives rejected

- Reusing `SpendTotal`: that type intentionally combines measured and estimated amounts.
- Inferring adapter and model identity from file names or today's profile: it would be misleading, so they are reported as unavailable.
- Transcript inspection or character-to-token estimates for prompt-only, tool-schema and file-read attribution: they stay unavailable until adapters supply versioned counters with provenance.

## Consequences

The report cannot yet attribute spend to adapter, model, prompt, tool schema or file reads.
Tests run against real temporary stores and attempt files; a narrow consumer-owned store interface lets regression tests count receipt scans and supply legacy start receipts.
The unfiltered report reads attempts without scanning receipts, and the start-time filter indexes receipts in one store-wide scan.
