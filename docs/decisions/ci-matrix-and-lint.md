---
status: accepted
date: 2026-10-20
topic: ci
amended-by: ci-action-pins-and-race.md
formerly: D4, 0004-ci-matrix-and-lint.md
---

# CI builds, vets, tests and lints on Linux, Windows and macOS with a pinned golangci-lint

## Context

Guildhall runs on Linux, Windows and macOS, so a change must be checked on all three before it merges.

## Decision

CI runs build, vet, tests, a gofmt check and golangci-lint with its standard linter set on `ubuntu-latest`, `windows-latest` and `macos-latest`.
It runs for every pull request, every push to `main` and manual dispatches for any ref.
Main runs have independent concurrency groups, so later pushes or dispatches neither replace pending runs nor cancel running ones; other refs share a per-ref group that cancels superseded runs.
golangci-lint is pinned to an exact version so lint results do not change underneath a pull request.
`.gitattributes` checks text out with LF endings so formatting checks agree on Windows.

## Consequences

Every pull request waits for the slowest of the three platforms ([Windows CI speed](windows-ci-speed.md)).
Updating golangci-lint is a deliberate change of its pin.
[CI action pins and race](ci-action-pins-and-race.md) extends the exact pin to every action and adds a Linux race step.
