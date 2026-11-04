---
status: accepted
date: 2026-11-04
topic: ci
tasks: d196
amends: ci-action-pins-and-race.md, ci-race-shards.md
---

# CI restores a rolling Go build cache and runs every `go test` step with `-count=1`

## Context

setup-go's own cache restored `GOCACHE` and `GOMODCACHE` under a key built from the runner OS, the Go version and the hash of `go.sum`, and saved nothing on an exact hit.
So every job compiled from a snapshot taken after `go.sum` last changed, which grew staler as the code moved ([CI speed evidence](../evidence/ci-speed-2026-11.md), H5).
The Test, managed and [race](ci-race-shards.md) steps also ran `go test` without `-count=1`, so a fresher build cache would let them replay `(cached)` passes instead of re-running the tests.

## Decision

- **`-count=1` on every `go test` step.**
  The Test, managed and race steps in `.github/workflows/ci.yml` run `go test -count=1`, as the timing-policy step already did.
  The rolling cache requires `-count=1` on every `go test` step in CI; a step that relies on cached test results needs the Guildmaster's separate H5b decision.
  `-count=1` landed in its own commit, ahead of the cache.
- **setup-go's cache is off.**
  Both `actions/setup-go` steps have `id: setup-go` and `cache: false`, on every OS.
  [`release.yml`](../../.github/workflows/release.yml) keeps setup-go's default cache.
- **Paths come from Go.**
  A `go-paths` step writes `go env GOCACHE` and `go env GOMODCACHE` to its outputs, so no path is hard-coded per OS.
- **Every job restores.**
  `actions/cache/restore`, pinned at `55cc8345863c7cc4c66a329aec7e433d2d1c52a9 # v6.1.0`, restores both paths: in `test` before the gofmt check, in `race` before the shard guard, whose `go list ./...` needs modules.
  The key is `go-test-<os>-<go version>-<sha>` for `test` and `go-race<shard>-<os>-<go version>-<sha>` for each race shard, and `restore-keys` is the same key without the sha, so a job restores the newest entry for its job, OS and Go version.
  The job and shard part of the key keeps `test (ubuntu-latest)` and the three race shards from colliding on one immutable Linux key.
- **Only main pushes and `workflow_dispatch` runs save.**
  `actions/cache/save`, at the same pin and with the same paths and key, is each job's last step, with `if: github.event_name != 'pull_request'`.
  In `test` it runs after Lint, so lint's compiles are in the entry.
  Pull requests restore from main's scope and never save, so their entries do not evict main's under the 10 GB repository limit.
- **Windows takes the cache provisionally.**
  `test (windows-latest)` uses the rolling cache until post-merge timings show whether its restore and save cost less than the compile time they save.
  A follow-up records those timings, from at least two main runs, in `docs/evidence/ci-go-cache-2026-11-timings.md`.
  If restore plus save on Windows costs more than it saves, Windows goes back to setup-go's cache and keeps `-count=1`.

## Alternatives rejected

- Keeping setup-go's cache and only adding `-count=1`: the snapshot stays stale, so the compile time it should save is not saved.
- One key per OS shared by `test` and the race shards: cache keys are immutable, so the first job to save would win and the others would restore an entry built for different flags.
- Saving on pull requests: PR entries would evict main's under the repository limit, and a PR's cache is not visible to other PRs.
- Measuring Windows before the build: the measurement needs CI runs of a published branch, so it moved after merge.

## Consequences

Each job compiles only what changed since main's newest entry for that job, and every test is still re-run on every CI run.
The first main push after this change saves an entry on every leg; until then pull requests restore nothing and pay the cold path.
Go's own cache trimming bounds each entry; there is no explicit size cap.
