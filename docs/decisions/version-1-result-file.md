---
status: accepted
date: 2026-10-20
topic: runtimes
formerly: D19, 0019-version-1-result-file.md
---

# An attempt's version 1 result file is published by hard link

## Context

An Artisan attempt reports its result in a file that Guildhall reads, so a reader must never see a partial result, and a second writer must never replace one already published.

## Decision

The [result-file contract](../adapters.md#result-file) defines the schema, validation and publication requirements.
The result is published by hard link, which makes a complete result visible atomically without replacing an existing result on Linux, Windows and macOS.

## Consequences

Publication depends on the file system supporting hard links in the attempt directory.
