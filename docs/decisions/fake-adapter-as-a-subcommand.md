---
status: accepted
date: 2026-10-20
topic: runtimes
formerly: D21, 0021-fake-adapter-as-a-subcommand.md
---

# The fake Artisan is a subcommand of the guildhall binary

## Context

CI and lab runs need an Artisan that behaves predictably and makes no model calls, on every platform CI covers.

## Decision

The fake Artisan is `guildhall fake-artisan`, part of the same binary, so CI and lab runs need nothing else installed.
Its test behaviours are selected by `guildhall-fake:` lines in the prompt file, bounded to the outcomes and faults listed under [the fake adapter](../adapters.md#fake-adapter).

## Consequences

The release binary carries test-only plumbing, and the fake's behaviours are limited to the ones the adapter page lists.
