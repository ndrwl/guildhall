---
status: accepted
date: 2026-10-31
topic: release
tasks: d127
amends: release-upgrades.md
---

# The PATH launcher is its own program, installed from a guildhall-launcher build beside the release

## Context

[Release upgrades](release-upgrades.md) put a small fixed launcher on `PATH`, installed with the first release and replaced only by `guildhall release install --launcher`, which the design describes as replacing it "from the running release".
A release is one `guildhall` binary ([release builds](release-builds.md)); nothing in it carries a second program, and `go install …@<version>`, the install-each-merge routine, cannot embed a built binary in another.
The launcher must also stay small and must never act on a command itself, so it cannot be the full `guildhall` binary told by its location to behave differently.

## Decision

The launcher is `cmd/guildhall-launcher`, a separate small program over `internal/launcher`, built by `go install github.com/ndrwl/guildhall/cmd/guildhall-launcher@<version>` beside `guildhall`, as the same routine installs `guildhall`.
`guildhall release adopt` takes it from `--launcher <file>`, else `guildhall-launcher` beside the `guildhall` on `PATH`, else on `PATH`; `guildhall release install --launcher` from `--from <file>`, else `PATH`.
Both confirm it is a launcher before installing it: run with `GUILDHALL_LAUNCHER_PROBE=1`, a launcher prints `{"schema":"guildhall.launcher","version":1}` and dispatches nothing, while a release ignores the variable, so a release or another program is refused.
The launcher passes every argument through, so the probe uses the environment, which the launcher drops before running a release.

## Alternatives rejected

- The release binary acting as the launcher when it finds itself at `<home>/bin/guildhall`: the copy adopt leaves in place of the old `PATH` binary is not there, a hash comparison on every start is slow, and the launcher would no longer be small or fixed.
- Embedding the launcher in the release: `go install` builds from source and cannot.
- Publishing a launcher asset with each release: downloading releases is the follow-up to d127 (grill Q2); the launcher asset can join the release workflow with it.

## Consequences

A machine adopts after installing both programs; adopt refuses, naming the `go install` command, when it finds no launcher.
The release workflow still builds only `guildhall`; the download follow-up adds `guildhall-launcher_<os>_<arch>` beside it.
