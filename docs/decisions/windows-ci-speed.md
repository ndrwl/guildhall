---
status: accepted
date: 2026-10-22
topic: ci
formerly: D81, 0081-windows-ci-speed.md
---

# Tests skip the store's fsync, scale wait ceilings on CI and share fixtures, to keep Windows CI fast and steady

## Context

The Windows test job took three to four minutes and failed merges whenever a runner was slower than usual.
The diagnosis on 2026-10-22 found Windows time to be latency, not compute: the slow packages fsync a bbolt store on every commit or start many processes (git, PowerShell, test-binary children), each costing tens of milliseconds there against microseconds on Linux.
The red runs were runners three to ten times slower than normal, where `internal/launch` tests with fixed wall-clock bounds (a 2 s store lock wait, a 30 s `Run` return, a 30 s survivor) gave up.
Defender was already off and no test leaked a process.

## Decision

- **Test binaries skip the store's fsync.**
  `store.SkipSyncForTests` sets bbolt's `NoSync` for every store a test binary opens afterwards, and panics outside a test binary, so production keeps syncing every commit.
  The packages whose tests open stores call it from `TestMain`; none of them tests power-loss durability, which a test cannot observe, and a killed process loses nothing either way.
  `TestOpenSyncsEveryCommitByDefault` keeps the synced path pinned, and `cmd/guildhall`'s built binary runs synced end to end.
- **Wait ceilings scale on CI; behaviour does not.**
  `testenv.Ceiling(d)` is `d` times 5 when `CI` is set, so a slow runner makes a test slow rather than red, and a local failure still comes promptly.
  It wraps every fixed test ceiling of 5 s or more and the store lock waits of the tests' own opens, and test children sharing `internal/launch`'s `deadline` inherit the scale.
  The scaled ceiling stops at the larger of `d` and two minutes: unbounded, `cmd/guildhall`'s minute-long waits grew to five and ten minutes, and a stuck macOS run hit `go test`'s ten-minute package timeout, which lost the test's messages.
  It is not applied to graces a test waits out on purpose or to durations the code observes; the production lock wait, `store.DefaultLockTimeout`, is unchanged ([test timing](../test-timing.md)).
- **Fixtures are built once and copied.**
  Real git, shells and processes stay the subject of their seams' contract tests; only the repeated setup around them is shared.
  For example, `gittest.Repositories` and the forge tests copy git fixtures built once, and the shell round trips type every line into one run of each shell.

## Consequences

What CI runs is unchanged: every package on all three platforms, with the same required tools.

On instrumented runs ([timings](../evidence/windows-ci-speed-2026-10-22-timings.md)) the Windows `go test` took 249 s and 365 s on main and 91 s and 176 s with this change; Linux went from 52 s to 42-50 s and macOS stayed at about 100 s.
Tests no longer exercise fsync except where pinned, so a durability regression must be caught by that pinned test or the built binary.
The ubuntu `TempDir` cleanup flake (sustainability review 11, F41) was not addressed.
