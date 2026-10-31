---
status: accepted
date: 2026-10-21
topic: engine
formerly: D36, 0036-immutable-attempt-preparation.md
---

# An attempt's prepared files are published once and never replaced

## Context

A missing `launched.json` does not prove that no worker is reading an attempt's prepared files: `Launch` can be accepted while its acknowledgement is lost, or the engine can stop after `Launch` returns and before it writes the mark.
The interpreter used to remove `prompt/` and render it again from current sources before the launcher could refuse the live duplicate, so a changed role, guide, standard or skill rewrote a live worker's files and the later provenance described bytes the worker never read.

## Decision

An attempt's prepared prompt, input copies, skill copies and manifest are published once and never replaced.
The [attempt preparation contract](../interpreter.md#attempt-preparation) owns the publication, replay validation and refusal rules.
Invalid preparation is retained, because its files are evidence of what a worker may have read and only the Director can decide what happens to them.

## Alternatives rejected

- Checking that the owner is not alive before rendering again: it races with the launcher it asks, and identical re-rendered bytes still replace files a live worker has open.
- A manifest kept outside `prompt/`: publishing it and the directory would be two steps with a crash window between them.
- A new filesystem interface for validation: the fault cases are reproduced with real temporary files, so no substitutable boundary is needed, and validation stays private to the engine.

## Consequences

A replayed advance reuses the published preparation even when the sources have changed since, so a changed role or skill reaches only the next attempt.
