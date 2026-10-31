---
status: accepted
date: 2026-10-20
topic: steward
formerly: D11, 0011-repository-paths-are-absolute-in-the.md
---

# Repository paths are absolute in the owner machine's form

## Context

A Steward validates Director configs whose owner machine may run another OS than its own.
A repository `path` therefore cannot be judged by the validating host's own path rules.

## Decision

`internal/config` accepts a repository `path` that starts with `/` or with a drive letter followed by `/` or `\`, on every host.
Relative, drive-relative and backslash-prefixed UNC paths are refused; paths beginning with `/`, including `//`, are treated as POSIX paths.
Duplicate comparison cleans POSIX dot segments and, for drive paths, normalizes separators, drive-letter case and dot segments independently of the validating host.

## Alternatives rejected

- Validating against the validating host's own path form: a Steward on one OS would refuse a valid config for an owner machine on another.

## Consequences

Config validation proves only that a path is absolute in some supported form, not that it suits the owner machine.
A later Director layer that opens repositories on its own machine checks the path against that machine's form.
