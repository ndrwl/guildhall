---
status: accepted
date: 2026-10-20
topic: dependencies
formerly: D8, 0008-toml-parsing-for-copy-configuration.md
---

# Copy configuration is parsed with BurntSushi/toml

## Context

Copy configuration uses TOML (R1.11), which the Go standard library does not parse.

## Decision

Add `github.com/BurntSushi/toml` v1.6.0 for `internal/config`, following the policy of [adding a dependency when a feature needs it](standard-library-only-for-the-foundation.md).
Its typed decoding and undecoded-key metadata let the configuration parser populate concrete Go types and reject unknown keys.
Workflow JSON continues to use the standard library's `encoding/json`.

## Alternatives rejected

- A custom TOML parser: it would have to be written and maintained for no behaviour the library lacks.

## Consequences

The binary gains its first third-party module.
The dependency adds no execution or activation behaviour.
