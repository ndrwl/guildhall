---
status: accepted
date: 2026-11-04
topic: ci
tasks: d177
amends: ci-action-pins-and-race.md
amended-by: ci-go-cache.md
---

# CI runs the race detector as three parallel shard jobs, `race (1)`–`race (3)`

## Context

The [race step](ci-action-pins-and-race.md) ran `go test -race ./...` inside `test (ubuntu-latest)`, after every other step.
On main run 41737488076 that step took 15.5 min of the job's 23.5 min, against 14 min for `test (windows-latest)` and 11.3 min for `test (macos-latest)`, so it was CI's critical path.
With the up-to-date branch rule every pull request re-runs CI after the one ahead of it merges, so each minute is paid once per queued pull request.

## Decision

- **Three race jobs.**
  The race step leaves the `test` job and becomes the `race` job, a matrix over `shard: [1, 2, 3]` named `race (${{ matrix.shard }})`, so its checks are `race (1)`, `race (2)` and `race (3)`.
  Each runs on `ubuntu-latest` with `GUILDHALL_CI_REQUIRE=pwsh`, the ubuntu matrix value, so [required skips](ci-required-skips.md) still fail.
  The jobs have no `needs:`, so their check runs appear as soon as the workflow starts and Guildhall's validate step never sees a finished rollup that lacks them.
  Coverage is unchanged: every package runs under `-race` on Linux once.
- **The shard count is fixed at three and the shards are numbered.**
  Rebalancing edits only [`.github/race-shards.txt`](../../.github/race-shards.txt), one `<shard> <import path>` line per package, and never renames a check, so branch protection names stay valid.
  The starting balance is in [the timing evidence](../evidence/ci-race-shards-2026-11-timings.md).
- **A guard in every shard.**
  Each race job first runs `bash .github/race-shards.sh check`, which fails, naming the package, when a package of `go list ./...` is in no shard or in more than one, when a listed package does not exist, or when a shard number is not 1–3.
  A new package nobody has added to the list turns all three race checks red.
  The guard is a step, not its own job, so it adds no check name.
- **Branch protection is the Guildmaster's.**
  After this change merges, the Guildmaster adds `race (1)`, `race (2)` and `race (3)` as required status checks on main; the three `test (…)` checks and the up-to-date rule stay.
  Until then a red race shard still sends a Guildhall task back to build, since validate and the merge queue fail on any failed check, but a merge made outside Guildhall is not blocked.

## Alternatives rejected

- Two shards, or a count chosen after measuring: the check names would not be known before the change, and the third shard gives margin for slower runners and test growth for about 1 min of setup on a parallel runner.
- Splitting `./internal/cli` across shards by `-run` patterns: it needs a different completeness guard than a package list, and is left for its own decision if that package alone becomes too slow.
- The guard as a separate job the shards `needs:`: it would add a check name and delay the shards' check runs.

## Consequences

`test (ubuntu-latest)` no longer runs the race detector, so a red race run no longer skips lanterns and lint on Linux.
CI's wall time becomes about the slowest of the Windows job and the slowest race shard.
Adding a package means adding a line to `.github/race-shards.txt` ([CONTRIBUTING](../../CONTRIBUTING.md)).
