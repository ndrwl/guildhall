---
status: accepted
date: 2026-10-20
topic: foundation
formerly: D1, 0001-one-go-module-and-one-binary.md
---

# The repository is one Go module that builds one binary

## Context

The foundation needed a module and package layout before any feature landed.

## Decision

The repository is the single Go module `github.com/ndrwl/guildhall` with one binary, `guildhall`, built from `cmd/guildhall`.
Packages live under `internal/` until something outside the module needs them.

## Consequences

Every feature ships in the one binary, so a release is one file per platform ([release builds](release-builds.md), [release skills root](release-skills-root.md)).
Nothing outside the module can import Guildhall's packages until one is deliberately moved out of `internal/`.
