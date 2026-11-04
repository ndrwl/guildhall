---
status: accepted
date: 2026-11-04
topic: ci
tasks: d203
amends: windows-ci-speed.md, ci-matrix-and-lint.md, ci-action-pins-and-race.md
---

# CI tests platform-neutral packages on Linux only

## Context

The `Test` step of `test (windows-latest)` and `test (macos-latest)` ran `go test ./...`, proving again packages whose code is the same on every operating system.
H7 of the [CI speed report](../evidence/ci-speed-2026-11.md) (§5.1, §6, §7) proposed running those packages on Linux only.
Since [Windows CI speed](windows-ci-speed.md), most of what is left to save is `internal/git` on Windows (45.2 s and 86.9 s on its two instrumented runs), with `internal/forge` (11.2 s and 19.7 s) and `internal/store` (4.6 s and 16.0 s) after it, and about a third of that on macOS ([timings](../evidence/windows-ci-speed-2026-10-22-timings.md)).
The report expected about 0.5–2 min saved on Windows and 0.2–0.5 min on macOS; the long pole, `internal/cli` and `internal/interp`, stays.
The Guildmaster chose H7 knowing it reduces coverage.

## Decision

- **A package is neutral by its own files.**
  [`.github/neutral-packages.sh`](../../.github/neutral-packages.sh) lists a package of `go list ./...` as neutral when none of its own `.go` files, `_test.go` included, has a `//go:build` or `// +build` line, has a GOOS or GOARCH filename suffix (the values of `go tool dist list`), or references `runtime.GOOS`.
  It reads every `.go` file in the package directory rather than `go list`'s file lists, which depend on the current GOOS.
  `neutral-packages.sh tested` prints the other packages; the two lists together are `go list ./...`.
- **Imports are not considered.**
  OS-specific code a neutral package imports is proved on Windows and macOS only through the packages that stay.
  For example, `internal/adapter/codex` is neutral, so its tests no longer run the Windows stub path of the OS-specific `codextest` on Windows; `codextest`'s own users that stay still do.
  A transitive rule would keep `codex`, and through `atomicfile`, `endpoint` and `release` imports most of `git`, `store` and `forge`, which removes most of the saving.
- **The list is computed on every run.**
  The `Test` step runs the script first on every leg, so a broken classifier fails every pull request, ubuntu included.
  Ubuntu prints the neutral list and runs `go test ./...`.
  Windows and macOS print the skipped packages, fail if either list is empty, as the race shards' guard does, and run `go test` on the tested list.
  `GUILDHALL_CI_REQUIRE` stays on the step; every package that uses [required skips](ci-required-skips.md) (`internal/herdr`, `skills`, `internal/adapter`) and `internal/testenv` is OS-specific, so none of them leaves a leg.
- Nothing else in the `test` job changes: gofmt, the timing-policy step, Build, Vet, the managed `./internal/cli` step, Lanterns and Lint still run on all three operating systems, and the check names stay `test (<os>)`.

## Alternatives rejected

- A transitive rule (`go list -test -deps` against the OS-specific set): see above.
- A checked-in list with a guard like `race-shards.sh check`: the computed list stays current without anyone editing it.
- A separate job for the neutral packages: it would add a check name and need a branch-protection change.

## Consequences

Windows and macOS no longer run the tests of the neutral packages, so these go unproved there:

- git's CRLF output and the Windows path, case and short-name handling of `internal/git` (`isWorktree`, `same`);
- bbolt's `LockFileEx` locking in `internal/store`; its fsync loss is small because `store.SkipSyncForTests` already turns fsync off in tests ([Windows CI speed](windows-ci-speed.md));
- file URLs and real `git` and `gh` runs in `internal/forge`;
- Windows `filepath` and `IsLocal` handling (drive letters, `\`, reserved names) in `internal/backup`, `internal/web` and `internal/doccheck`.

Every package is still proved on Linux by `test (ubuntu-latest)` and under `-race` by the [race shards](ci-race-shards.md), and Build and Vet still compile every package and its tests on all three operating systems.
This replaces "every package on all three platforms" in [Windows CI speed](windows-ci-speed.md) and the plain `Test` step "on all three operating systems" in [CI action pins and race](ci-action-pins-and-race.md); [CI matrix and lint](ci-matrix-and-lint.md)'s three-platform check now runs the full suite on Linux only.
Adding an OS-specific file or a `runtime.GOOS` reference to a package moves it back onto all three operating systems ([CONTRIBUTING](../../CONTRIBUTING.md)).
The measured `Test`-step times on Windows and macOS are recorded in this change's pull request against recent main runs.
