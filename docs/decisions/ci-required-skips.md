---
status: accepted
date: 2026-10-21
topic: ci
formerly: D46, 0046-ci-required-skips.md
---

# A CI runner's required tools turn their skips into failures

## Context

Tests that need an installed shell or Windows skip by name when the host lacks it.
`go test` without `-v` prints nothing for a skip, so a CI log cannot show that a case ran where it was meant to.

## Decision

Each CI job sets `GUILDHALL_CI_REQUIRE` to the tools and platform its runner supplies, and those skips go through `internal/testenv`, which fails the test when the missing name is required.
Local runs leave it unset and skip as before.
Only the skips for an absent `pwsh`, `powershell` or `ksh` and the Windows-only cases are covered.
The requirable names are a fixed list, so a misspelt requirement fails every runner instead of quietly requiring nothing.

## Alternatives rejected

- Switching CI to `go test -v`: a verbose log shows a skip only to someone who reads it, while a failure stops the job.
- Covering every skip: the cgroup, symbolic-link, root, path-padding and Go-toolchain skips depend on host configuration rather than a tool the runner promises, and stay plain skips.

## Consequences

A runner image that loses a promised tool turns CI red rather than silently running fewer cases.
New steps that run tests must carry the same `GUILDHALL_CI_REQUIRE`, as the [race step](ci-action-pins-and-race.md) does.
