---
status: accepted
date: 2026-10-20
topic: dependencies
formerly: D3, 0003-standard-library-only-for-the-foundation.md
---

# The foundation uses only the standard library, and dependencies arrive with the feature that needs them

## Context

The foundation, `version` and `doctor`, needed no third-party code, and every module added is a dependency to review and keep current.

## Decision

The foundation (`version` and `doctor`) uses only the standard library.
Third-party modules arrive with the first feature that needs them, each recorded in its own decision (S7).

## Consequences

Each dependency has a record saying why it was added, such as [TOML parsing](toml-parsing-for-copy-configuration.md) and [bbolt](bbolt-for-the-store.md).
