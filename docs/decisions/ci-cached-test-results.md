---
status: accepted
date: 2026-11-04
topic: ci
tasks: d202
amends: ci-go-cache.md
---

# Pull requests replay main's cached test passes; main and `workflow_dispatch` runs expire them and re-run every test

## Context

The [rolling Go cache](ci-go-cache.md) ran every `go test` step with `-count=1`, so every test re-ran on every CI run.
H5b of the [CI speed evidence](../evidence/ci-speed-2026-11.md) is its coverage-reducing option: pull requests reuse passes for packages they do not change, up to ≈5 / ≈4 / ≈3 min on ubuntu / windows / macos, and every merge is still fully proved on main.
Go records a test result only when every flag is cacheable, and `-count` is not (`go help test`), so a run with `-count=1` saves no passes for anyone to replay.

## Decision

- **Clear, then run without `-count=1`.**
  The Test, managed and race steps in `.github/workflows/ci.yml` run `go test` without `-count=1` on every event.
  In `test` and in each race shard, an "Expire cached test results" step runs `go clean -testcache` after "Restore Go cache" and before the tests, with `if: github.event_name != 'pull_request'`.
  So main pushes and `workflow_dispatch` runs re-run every test and save each pass in their entry, and pull requests, which never clear and never save, replay those passes for packages they do not change.
- **The clear and save conditions are identical.**
  Each clear step's `if:` is literally the same as its job's "Save Go cache" step's, and a comment on the clear step says so.
  Any run that saves an entry has re-proved every pass in it.
- **The timing-policy step keeps `-count=1`** on every event: it had it before the rolling cache, it is one small package, and none of the saving is there.
- **Windows.** If `test (windows-latest)` goes back to setup-go's snapshot cache ([ci-go-cache.md](ci-go-cache.md)), its Test and managed steps keep `-count=1` on pull requests as well, since that snapshot is not re-proved on every main push.

## Accepted risks

The coverage loss is accepted as the evidence states it: "Risk: medium, and wider than today's gap. A cached pass is not re-proved against a new runner image, git version or PowerShell, and a required-skip outcome under `GUILDHALL_CI_REQUIRE` is reused as it was."
A green `ok <pkg> (cached)` in a pull request's log was proved on main, not on that pull request.
In every case below, the next main push's full run catches the problem.

- **R1.** A cached pass is not re-proved against a new runner image, git or PowerShell version; a required skip is replayed as recorded.
- **R2.** Inputs read only by subprocesses (`git`, `pwsh`) are not part of Go's test cache key.
- **R3.** A flaky test that passed on main looks green on a pull request without running.
- **R4.** A pull request whose base main run has not saved yet restores an older entry; it re-runs more, which is slower but not unsafe.
- **R6.** If the clear and save conditions drift apart, a replayed pass could be saved without being re-proved. Only the comment and this record guard against it.

Failed tests are never cached and a failing run does not save, so a pull request never replays a failure.
Configurations stay separate: managed runs read `GUILDHALL_TEST_DIRECTOR_MODE`, `GUILDHALL_CI_REQUIRE` is fixed per OS and for race, `-race` builds a different binary, and each job, shard and OS has its own cache key.

## Alternatives rejected

- `-count=1` on main pushes only, as the task worded it: main would save no passes, so pull requests would replay nothing and gain no speed.
- Running main's tests twice, once with `-count=1` and once without to fill the cache: it doubles main's test time.
- Clearing only on `push`: a `workflow_dispatch` run also saves, and would carry forward passes it never re-proved.
- Keeping `-count=1` on the race step: one rule for every `go test` step is simpler to state and check, and the race shards gain the same saving.

## Consequences

The first main push after this change re-runs every test and saves its entries; pull requests after it show `ok <pkg> (cached)` for packages they do not touch.
No timing evidence is recorded for this change.
