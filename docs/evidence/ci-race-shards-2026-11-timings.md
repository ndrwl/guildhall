# Race shard timings — 2026-11-04

The evidence behind the first split in `.github/race-shards.txt` ([ci-race-shards](../decisions/ci-race-shards.md)).

## What was measured, and what could not be

The plan asked for per-package times under `GUILDHALL_CI_REQUIRE=pwsh go test -race -json ./...`.
That run was not possible on the build machine: it has no C compiler, so `go env CGO_ENABLED` is `0`, and `go test -race` stops with `go: -race requires cgo; enable cgo by setting CGO_ENABLED=1`.
It has no `pwsh` either, so `GUILDHALL_CI_REQUIRE=pwsh` would fail the `pwsh` skips by design.
No race build time could be measured.

The split is therefore balanced on the plain suite as a proxy, taking `Elapsed` from each package's final `pass`/`fail`/`skip` event of:

```sh
go test -json -count=1 ./...
```

- Machine: Linux 6.18 (WSL2), 16 CPUs, go1.27.2, `CGO_ENABLED=0`, no `CI` set, `GUILDHALL_CI_REQUIRE` unset.
- Run: 2026-11-04 at the task's base `2f7d34f8`; every package passed.
- Packages run in parallel (`-p 16`), so a time includes contention with the others, as it will within a shard.

The race detector slows each package by a roughly similar factor, so relative weights are what the split uses, not absolute minutes.
For scale: on main run 41737488076 the whole race step took 15.5 min on one ubuntu runner.

## Per-package times (plain suite, seconds)

| Seconds | Package | Shard |
| ---: | --- | :---: |
| 223.9 | internal/cli | 1 |
| 185.6 | internal/launch | 2 |
| 150.8 | internal/interp | 3 |
| 129.0 | cmd/guildhall | 3 |
| 93.5 | internal/artisan | 2 |
| 28.4 | internal/forge | 1 |
| 27.0 | internal/store | 1 |
| 26.5 | internal/git | 1 |
| 13.6 | internal/steward | 2 |
| 10.0 | internal/adapter/claude | 2 |
| 8.1 | internal/herdr | 2 |
| 7.9 | internal/release | 2 |
| 49.6 | the other 44 packages, each under 5 s (10 have no tests) | 3 |

Shard sums: shard 1 ≈ 306 s, shard 2 ≈ 319 s, shard 3 ≈ 330 s, of about 955 s in all.

## The split

The plan's starting point was `internal/cli` alone, then `internal/interp` with `internal/store`, then the rest.
The measurement changed it: `internal/store` takes 27 s, while `internal/launch` (186 s), `cmd/guildhall` (129 s) and `internal/artisan` (93 s) would all have fallen in shard 3, about 553 s against 224 s for shard 1 and 178 s for shard 2.
Instead the five slowest packages are kept apart, each shard holds about a third of the total, and `internal/cli` stays with only three mid-sized packages beside it.

If the whole race step's 15.5 min divides in the same proportions, each shard's tests take about 5–5.5 min, plus about 1 min for checkout, setup and a cold race compile, which leaves room under the 10 min target for runners up to about 1.5x slower than that run.
These are estimates from a proxy; the real figures are the `race (1)`–`race (3)` job times of this change's own CI run, recorded in its pull request, and `go test` prints each package's race time in those job logs for the next rebalance.
