---
status: accepted
date: 2026-10-21
topic: artisan
formerly: D44, 0044-contract-tdd-skill.md
---

# Build and fix stages use the guildhall-contract-tdd skill, adapted from upstream tdd

## Context

Build and fix stages need a test-first method, and the release adapts upstream skills as the [release skills root](release-skills-root.md) records.
Upstream wrote `tdd` for a session paired with a person, while a build stage runs unattended against an accepted spec.

## Decision

The release ships `guildhall-contract-tdd`, which teaches the method in [contracts and tests](../contracts.md) rather than a variant of it: the seams are the contracts the accepted spec names, a contract the spec did not name is an open question rather than a new interface, and a test double passes the same contract test function as the real implementation.
The upstream call to a separate design skill is replaced by a `contracts.md` reference file inside the skill, so no skill depends on another being present.
The result records the red and green runs and lists pins separately, because the review standard (S2) requires the pull request description to show both and the contract method forbids passing a pin off as red-then-green.
The optional `check` scripts run only `gofmt -l .`, `go vet ./...` and `go test ./...`, with `GOPROXY=off` and `GOTOOLCHAIN=local`, so they cannot download modules or toolchains.
A test runs both twins against the test binary acting as `go` and `gofmt` and requires the same calls and the same outcome, so the twins cannot drift.

## Alternatives rejected

- Upstream's instruction to agree seams with a user: a build stage has nobody to ask, and the accepted spec already fixed them.
- `go build` and `golangci-lint` from the contributing guide in the scripts: `go vet` already builds every package, and the linter is a pinned CI tool an Artisan's machine may not have.

## Consequences

A missing module fails the script instead of reaching the network.
A repository in another language replaces the scripts with a Steward-managed override of the skill.
Nobody has yet run the method unattended against a spec.
