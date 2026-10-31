---
status: accepted
date: 2026-10-20
topic: release
amended-by: version-from-build-info.md
formerly: D5, 0005-release-builds.md
---

# Releases are five static cross-compiled binaries with checksums, published only from a version tag

## Context

A release must run on every supported platform without a C toolchain, and the future bootstrap flow (R1.51) needs checksums to verify what it downloads.

## Decision

The release workflow cross-compiles five targets with `CGO_ENABLED=0` and `-trimpath`: linux/amd64, linux/arm64, windows/amd64, darwin/amd64 and darwin/arm64.
Binaries are named `guildhall_<os>_<arch>` (with `.exe` on Windows) and listed in `SHA256SUMS`.
Only a pushed `vMAJOR.MINOR.PATCH[-PRERELEASE]` tag publishes a GitHub release; a manual run builds and checksums only, as a dry run.
The workflow injects the version through `-ldflags -X github.com/ndrwl/guildhall/internal/cli.version`: the tag for tag builds, or `dev-<first 12 commit SHA characters>` for manual branch builds.

## Consequences

Dependencies must be pure Go, as [bbolt](bbolt-for-the-store.md) is, to keep `CGO_ENABLED=0`.
The publication policy is guarded by the [release policy test](release-policy-regression-test.md).
Builds without an injected version originally reported the module version Go stamps into the binary, or `dev`; [version from build info](version-from-build-info.md) replaced that with the build's `vcs.*` revision, while an injected tag still wins.
