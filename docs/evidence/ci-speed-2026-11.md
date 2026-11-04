# Where CI time goes, and ranked ways to make CI faster (d178, 2026-11)

Task d178, build-1 and build-2, 2026-11-04. Research for the plan in d178 plan-1. This document changes nothing: `.github/workflows/ci.yml`, the Go code, the tests and branch protection stay as they are. Each change the Guildmaster picks becomes its own follow-up task.

## 1. Question and answer up front

**Question.** Where does a CI run's time go on ubuntu, windows and macOS, and which PR-sized changes would cut the critical path the most?

The critical path is the slowest leg, which sets the run's wall time. Under the up-to-date branch rule, every PR waiting in line pays that time again. d177 takes the race run off the ubuntu leg (15.5 of its 23.5 minutes). After that the critical path is Windows (≈14 min), then macOS (≈11.3 min), so every saving below is measured against those two legs.

**Answer.** On Windows and macOS the two `go test` steps take about 87–90% of the time. On ubuntu they take about 31%, and the race run takes most of the rest. The managed step runs only `internal/cli`, a package whose 600 top-level tests run one after another. That package is also the Test step's long pole on Linux [local], and was on macOS on 2026-10-22. The Windows Test step's long pole is not known (§4.2). The research ranking put H1, then the managed rerun, then sharding first. The new local data adds H2 and re-sizes H3. Do these first:

1. **Record per-package and per-test timings in every CI run (H1, S).** It saves nothing by itself, but this build could not read any per-step, per-package or per-test CI timing (§2). Every other estimate here should be re-checked against its data.
2. **Stop `TestQualityJudgeConfiguredLockWait` waiting out the default 30 s store lock (H2, XS).** In the local run, two of its subtests each waited the 30 s `config.DefaultLockWait` although the test configures `lock_wait = "1s"`. That is 60 s on `internal/cli`'s serial path, and the managed step pays it again. Expected saving: **≈1–2 min on each leg**, with no coverage loss. The cause may be a product bug (§5.5).
3. **Make the managed rerun run only the tests that a managed copy changes (H3, M).** Expected saving: **≈2–2.5 min on Windows and macOS**, with no coverage loss. In the local managed run, only 191 of the 600 top-level `internal/cli` tests went through `maybeManage`, and they took 89 s of the 203 s of test time (§4.3). Its coverage-reducing alternative, H3b (run the managed step on ubuntu only), saves the whole step (≈4.3 min Windows, ≈4.4 min macOS).
4. **Run `internal/cli` as concurrent shards (H4, S–M).** Expected saving: **≈1–3 min on Windows and macOS** in each of the two test steps, with no coverage loss. The size depends on H1's data.

The savings overlap. H2's managed-step half is already inside H3's, because `TestQualityJudgeConfiguredLockWait` is not one of the converted tests. Together the four could take roughly 4–6 minutes off a 14-minute Windows leg without dropping any test from any platform. Each estimate is labelled with its source in §6.

One finding is a coverage gap rather than a speed-up. The Test, managed and race steps run without `-count=1`, so CI can already reuse cached test passes from setup-go's cache snapshot (§5.3). H5 adds `-count=1`.

## 2. Limits

- **Base commit.** Every file and line citation was checked against `2f7d34f8`, this build's base.
- **No GitHub API.** `gh api` and `gh-axi run view` for run 41737488076 needed a permission this session does not have, and both were refused before they ran. So the report has **no per-step timings** for that run beyond the step totals the task and grill recorded, **no second main run**, and **no per-package or per-test CI timings** on any OS. Those figures are **missing**, not estimated.
- **Local run, Linux only.** `go` was available, so this build ran `go test -count=1 -json ./...` once at `2f7d34f8` on a 16-CPU Linux (WSL2) machine with go1.27.2, standalone mode, `CI` unset. All tests passed. It then ran `internal/cli` once in managed mode (`-director-mode=managed`), with a one-line `t.Log` temporarily added to `maybeManage` to record which tests it converts. The line was removed before committing (§4.3). Local times are not CI times: the runner, the CPU count and `testenv.Ceiling`'s CI scaling all differ (`docs/contracts/testenv.md:19`). They show the **shape**, meaning which packages and tests are long, not the minutes. Windows and macOS could not be run. Build-2 then ran plain `go test ./...` twice in a row, without `-count=1`, to check which packages Go's test cache can reuse (§5.3).
- **Labels.** Each figure is marked **[CI]** when it comes from run 41737488076's step totals (task detail and grill), **[local]** when it comes from this build's run, **[2026-10-22]** when it comes from `windows-ci-speed-2026-10-22-timings.md` (older code, so stale), or **[est.]** when it is an estimate derived from those, with the reasoning given.

## 3. How CI runs today

`.github/workflows/ci.yml` has one job, `test`, with a matrix of `ubuntu-latest`, `windows-latest` and `macos-latest` and `fail-fast: false` (`ci.yml:17-22`). Each leg runs these steps one after another:

| # | Step | OS | `ci.yml` |
|---|---|---|---|
| 1 | checkout | all | 37-39 |
| 2 | setup-go, `go-version-file: go.mod`, with setup-go's default cache | all | 40-42 |
| 3 | gofmt check | all | 43-50 |
| 4 | Test timing policy: `go test ./internal/testenv -run '^TestTimingPolicy' -count=1` | all | 51-52 |
| 5 | Build: `go build ./...` | all | 53-54 |
| 6 | Vet: `go vet ./...` | all | 55-56 |
| 7 | **Test**: `go test ./...` with `GUILDHALL_CI_REQUIRE`, **no `-count=1`** | all | 57-60 |
| 8 | **Test Director suites managed**: `go test ./internal/cli` with `GUILDHALL_TEST_DIRECTOR_MODE=managed`, **no `-count=1`** | all | 63-67 |
| 9–11 | setup-python 3.12, PyYAML, `.github/tests/test_release.py` | ubuntu | 68-77 |
| 12 | **Test with race detector**: `go test -race ./...` (d177 moves this) | ubuntu | 78-82 |
| 13 | setup-node 24 | all | 83-85 |
| 14 | Lanterns `node --test internal/lanterns/plugin/tests/*.test.mjs` | all | 86-87 |
| 15 | golangci-lint v2.15.0 through golangci-lint-action v9 | all | 88-91 |

The workflow has no `paths` or `paths-ignore` filter and no `actions/cache` step, so its only Go cache is setup-go's default. Only the timing-policy step (`ci.yml:52`) passes `-count=1`. The Test step (`ci.yml:60`) and the managed step (`ci.yml:67`) do not, so they can reuse cached test passes from the `GOCACHE` that setup-go restores (§5.3).

The decision records behind this shape:

- `docs/decisions/ci-matrix-and-lint.md:13`: "a change must be checked on all three before it merges". Line 25: "Every pull request waits for the slowest of the three platforms."
- `docs/decisions/ci-action-pins-and-race.md:30`: new steps sit in the existing `test` job so they are part of the required check "without editing branch protection". Line 36 rejects a new job because "it would need a branch-protection change to be required".
- `docs/decisions/windows-ci-speed.md:13`: Windows time is "latency, not compute" (fsync, git, PowerShell and test-binary process starts). Line 34: "every package on all three platforms". Line 26: the scaled ceiling stops at two minutes to stay inside `go test`'s ten-minute package timeout.
- `docs/director-modes.md:154`: "Every Director suite that loads config runs twice, through one shared helper", once standalone and once managed.

## 4. Where the time goes

### 4.1 Per job and per step, run 41737488076 [CI]

| Job | Total | Test (`./...`) | Managed (`./internal/cli`) | Race | Everything else |
|---|---:|---:|---:|---:|---:|
| ubuntu | 23.5 | 4.4 | 2.8 | 15.5 | ≈0.8 |
| windows | 14.0 | 8.3 | 4.3 | — | ≈1.4 |
| macos | 11.3 | 5.4 | 4.4 | — | ≈1.5 |

Minutes. The task detail gives each leg's total, ubuntu's race step and the Windows and macOS Test and managed steps. The ubuntu Test (4.4) and managed (2.8) figures come from the d178 grill (grill-1), not from the task detail. "Everything else" is derived: the total minus the named steps. It is the most that checkout, setup-go, gofmt, the timing-policy test, build, vet, Python, setup-node, the Lanterns tests and lint can cost together on that leg.

**Missing:** each of those smaller steps' own time, and any second main run to show runner variance. Hosted runners vary by up to about 2x between runs of the same commit (`windows-ci-speed-2026-10-22-timings.md:60`).

What follows from this:

- The two `go test` steps are 87–90% of the Windows and macOS legs. All setup, build, vet and lint steps together are at most ≈1.4–1.5 min per leg.
- After d177, nothing that speeds up only ubuntu shortens the wall time.
- The managed step reruns one package, `internal/cli`, and costs 4.3–4.4 min on Windows and macOS. So **`internal/cli` alone takes about 4.3 min on Windows and macOS** [est., from the managed step, which runs only that package].

### 4.2 Per package and slowest tests, local Linux [local]

`go test -count=1 -json ./...`: wall 250 s (01:07:22–01:11:32 UTC), 56 packages, 933 s of package time, 2,197 top-level tests, no failures.

| Package | Seconds | 2026-10-22 ubuntu, main a |
|---|---:|---:|
| internal/cli | 220.2 | 26.8 |
| internal/launch | 185.7 | 30.5 |
| internal/interp | 138.2 | 8.3 |
| cmd/guildhall | 115.4 | 10.0 |
| internal/artisan | 112.7 | 16.6 |
| internal/store | 24.0 | 1.3 |
| internal/git | 23.7 | 5.5 |
| internal/forge | 20.8 | 2.9 |
| internal/steward | 15.6 | 1.9 |
| internal/adapter/claude | 10.8 | 2.6 |
| every other package | ≤ 8.2 each | |

`internal/cli` is the long pole: the wall time is its 220 s plus compiling. Its 600 top-level tests run serially. Only `cmd/guildhall/director_safety_test.go:464,467` and `internal/interp/history_replay_test.go:193` call `t.Parallel()` anywhere in the repo.

Slowest top-level tests across the repo:

| Seconds | Package | Test | Why it is slow |
|---:|---|---|---|
| 63.6 | internal/artisan | TestStopContract | three ladder cases at ≈20 s each (L7, L15 startup, L18) |
| 62.1 | internal/cli | TestQualityJudgeConfiguredLockWait | two `/store` subtests at 30.0 s each, with `lock_wait = "1s"` configured (§5.5) |
| 50.3 | internal/launch | TestPaneOperationsTimeoutRetainsClaimWithoutRetryOrStop | five phases, each waiting its 10 s `StartTimeout` (Unix only, `presentation_unix_test.go:279-283`) |
| 44.3 | internal/launch | TestStopContractRealFiles | stop ladder |
| 43.5 | internal/launch | TestStopContractFakeArtisan | stop ladder |
| 42.6 | internal/artisan | TestFakeStopContract | two ladder cases at ≈20 s each (L7, L18), even with the fake unit |
| 20.4 | internal/launch | TestRunStopsWhenTheContextEnds | |
| 15.4 | internal/cli | TestAdvancePreflightLockWait | three subtests at ≈5 s each: an intentional 5 s `lock_wait` (`advance_lock_wait_test.go:14-17`) |
| 10.5 | internal/cli | TestRunCleanupReportsAnOrphanedWorker | |
| 10.1 | internal/cli | TestSessionEndReportsASurvivingProcess | |

The ladder cases wait out real stop rungs on purpose. The timing policy says "Real containment test ladders allow ten seconds per rung" (`docs/test-timing.md:24`), and those rungs are durations the code observes, which `Ceiling` does not scale (`docs/contracts/testenv.md:22`).

Within `internal/cli`, the seven slowest tests take 113 s of the package's 220 s. The other 593 average ≈0.18 s each.

**Missing:** the same tables for Windows and macOS. On 2026-10-22 the Windows long poles were `internal/interp`, `internal/git`, `internal/launch` and `internal/cli`, and on macOS it was `internal/cli` (≈90 s of a ≈100 s wall) [2026-10-22]. Which package is the Windows long pole now is unknown. The Windows Test step (8.3 min) is almost twice its managed step (4.3 min), so on Windows either another package takes longer than `internal/cli`, or `internal/cli` runs slower under contention with the other packages [est.].

### 4.3 The managed rerun, local Linux [local]

`go test -count=1 -json ./internal/cli -args -director-mode=managed` at `2f7d34f8`, with one temporary line in `maybeManage` (`internal/cli/dual_mode_test.go:34-40`): `t.Log("d178-managed-copy")` just before `manageCopy`. The line recorded which tests a managed run actually converts. It was removed before this report was committed, so the committed tree is `2f7d34f8`'s code.

- Wall 208 s (01:12:52–01:16:20 UTC). 600 top-level tests, 203 s of test time, no failures.
- **191 of the 600 top-level tests (32%) went through `maybeManage`. They took 89.3 s (44%) of the 203 s.**
- The other 409 tests behave the same in both modes, apart from `newRunDirectorAt` copying its fixture when `managedRun()` is true (`advance_test.go:88`). They took 114 s (56%), and the managed step runs them again for nothing. They include `TestQualityJudgeConfiguredLockWait` (62.0 s) and `TestSessionEndReportsASurvivingProcess` (10.0 s). Converted tests include `TestAdvancePreflightLockWait`, `TestRunCleanupReportsAnOrphanedWorker` and `TestTheLauncherSendsAStallWithoutAnAdvance`.
- A file-based selection would be less exact: 33 of the package's 126 test files call `newCopy`, `directorCopy` or `newRunDirectorAt` (`git grep -l`), but tests in other files reach them through shared helpers.

So the managed step spends about 56% of its test time on tests that a managed copy does not change [local]. Applied to the CI step totals, that is ≈2.4 min on Windows and ≈2.5 min on macOS [est.: 4.3 and 4.4 min × 0.56]. The binary build is part of the step and stays, so the real figure is somewhat lower, ≈2–2.5 min.

### 4.4 Change mix on main [local]

Of the last 200 first-parent commits on main (back to 2026-10-22), 193 touch something outside `docs/` and `*.md` (`git rev-list --count --first-parent HEAD~200..HEAD -- . ':(exclude)docs' ':(exclude)*.md'`). So 7 in 200 (3.5%) are docs-only.

## 5. Candidates assessed

### 5.1 Platform-neutral packages on Linux only

What it would do: Windows and macOS would skip `go test` for packages that have no OS-specific files and no `runtime.GOOS` reference, such as `internal/store`, `internal/git`, `internal/forge` and `internal/doccheck`.

Would it help the critical path? Only a little, and it carries the most coverage risk:

- Locally those four packages take 24, 24, 21 and under 1 s and are nowhere near the long pole [local].
- On 2026-10-22, `internal/git`, `internal/store` and `internal/forge` were 3 of the 7 slowest Windows packages [2026-10-22]. Taking them out would ease contention but would not remove the `internal/cli` long pole.
- Their Windows behaviour (fsync, git processes, file locks, path separators, CRLF) is what the Windows leg proves for them (`windows-ci-speed.md:13`). The change would also reverse `windows-ci-speed.md:34` ("every package on all three platforms") and `ci-matrix-and-lint.md:13`.

Ranked as H7, coverage-reducing.

### 5.2 Fold the managed-suite run into the main test run

A single `go test` process cannot run `internal/cli` in both modes, because the mode is read once per process from `GUILDHALL_TEST_DIRECTOR_MODE` or `-director-mode` (`internal/cli/dual_mode_test.go:26-31`). So folding means running a second process alongside the Test step instead of after it, for example backgrounding `go test ./internal/cli -args -director-mode=managed` while `go test ./...` runs, and failing the step if either fails.

- It removes the managed step's serial wait (≈4.3 min Windows, ≈4.4 min macOS [CI]), but adds contention: the two processes share the runner's CPUs and disk. Windows time is latency, not compute (`windows-ci-speed.md:13`), so overlapping wait-bound work should overlap well. The net saving is uncertain, ≈1.5–3.5 min [est.].
- It needs nothing from branch protection, because it is still inside the `test` job.
- The output of the two processes must stay separable so a failure stays readable.

H3 (run only the mode-dependent tests) gets most of this saving without the contention, and the two combine. Folding is kept as an option inside H3, not a separate rank.

### 5.3 Go build and test caching

- setup-go caches `GOCACHE` and the module cache, keyed on the OS, the Go version and the `go.sum` hash. On an exact key hit it does not save again, so the cache stays a snapshot from the first run after `go.sum` last changed, and goes staler as the code moves. This is from setup-go's documented behaviour. **Missing:** this run's setup-go and post-setup-go log lines, which would confirm hits and sizes.
- Build, vet and test-binary compiles are inside "everything else" (≤1.5 min) and the Test step. A rolling cache keyed on `github.sha` with `restore-keys` would keep them warm [est. 0.5–2 min per leg]. Windows cache extraction can eat much of that.
- **Test-result caching happens today.** `go test` in package-list mode without `-count=1` reuses a cached pass when the test binary (so the package and everything it imports), the files the test opened and the environment variables it read are unchanged. The Test, managed and race steps (`ci.yml:60,67,82`) all run without `-count=1`, so each can report `(cached)` for any package whose inputs match an entry in setup-go's restored snapshot. `managedRun` reads `GUILDHALL_TEST_DIRECTOR_MODE` through `os.LookupEnv` (`dual_mode_test.go:27`), so managed passes are cached under their own key, and `-race` builds a different binary, so race passes are too.
  - Every package can be cached. Locally at `2f7d34f8`, a second plain `go test ./...` straight after a first reported all 46 packages that have tests `(cached)` [local].
  - How much CI reuses now depends on how old the snapshot is. setup-go's key last changed at `d385b331` (2026-10-31, a `go.mod`/`go.sum` change). From there to `2f7d34f8` is 55 first-parent commits, and they touch almost every package directory, including `internal/testenv`, `internal/config` and `internal/store`, which most packages import (`git diff --dirstat=files,0 d385b331 2f7d34f8`). By an approximate check of in-module dependencies (`go list -test -deps`), only small leaf packages such as `internal/cilog`, `internal/clerk`, `internal/digest`, `internal/policies` and `internal/strictjson` can still hit at the base. Each takes under 4 s locally, so the minutes reused today are small [est.]. Right after the next `go.sum` change, though, the snapshot is fresh, and on the runs after it most unchanged packages will report `(cached)`.
  - So **CI already has a coverage gap**, and it is not under anyone's control: whether a package is re-proved depends on when `go.sum` last changed. A cached pass is not re-proved against a new runner image, git version or PowerShell. Under `GUILDHALL_CI_REQUIRE` a cached result is replayed as it was recorded. **Missing:** whether run 41737488076's step logs show any `(cached)` lines, and so whether its Test and managed minutes are already lower than a full run's.

Ranked as H5 (rolling build cache, which **adds** `-count=1` to the Test, managed and race steps, so every package is re-proved on every run) and H5b (cached test results on PRs, coverage-reducing). H5's `-count=1` half closes today's gap and can also land alone.

### 5.4 Path filters so docs-only PRs skip the Go jobs

- Only 3.5% of recent commits are docs-only (§4.4).
- Docs are under Go test: `internal/doccheck` checks links, the docs tree and decisions, and `internal/store`, `internal/render` and `internal/artisan` tests read `docs/`. A docs-only PR still needs at least `go test ./internal/doccheck/...`.
- A workflow-level `paths-ignore` would leave the three required `test (<os>)` checks pending, so a docs-only PR could never merge. It would have to be a job-level skip of the Go steps that still reports success.

Expected saving averaged over all PRs: under 0.5 min per run [est.: 3.5% × ≈12 min]. Ranked last, H9.

### 5.5 The slowest individual tests

From §4.2:

- **`TestQualityJudgeConfiguredLockWait`** (`internal/cli/quality_boundary_test.go:50-101`). Its `judge/store` and `judged/store` subtests hold the store's lock and run `quality judge` or `quality judged` with `lock_wait = "1s"`. Each one waited 30.0 s [local], which is `config.DefaultLockWait` (`internal/config/config.go:355`), not the configured 1 s. The test's own message says "want configured 1s wait within 4s", but it fails only after `time.Minute` (line 95), so it passes. Its sibling `TestAdvancePreflightLockWait` deliberately keeps its ceiling "below the default 30s store wait so ignoring lock_wait still fails" (`advance_lock_wait_test.go:15-16`). So the quality commands probably ignore `lock_wait` on one of their store opens. This build did not diagnose where. Fixing that and tightening the test removes ≈60 s from `internal/cli`'s serial path in **both** test steps, on every OS. In the managed step, the test is not a converted one (§4.3), so H3 would also remove it from there. The 30 s is the production lock wait, not a scaled test ceiling, so it costs the same on every runner. This is H2.
- **Stop-ladder contracts** (`internal/artisan` TestStopContract and TestFakeStopContract, and `internal/launch` TestStopContractRealFiles and TestStopContractFakeArtisan). About 40–60 s each, all waiting out real rungs. They are not the long pole locally, because they run in other packages alongside `internal/cli`. If H1 shows `internal/launch` or `internal/artisan` is the Windows long pole, shorter rungs for the fake harness are a candidate. The rung length is pinned by the timing policy (`docs/test-timing.md:24`), so that change would have to amend it. Part of H6.
- **`TestPaneOperationsTimeoutRetainsClaimWithoutRetryOrStop`** is Unix-only (`presentation_unix_test.go`), so it is on macOS's path but not Windows's. It waits five 10 s start timeouts that could perhaps run as parallel subtests. Part of H6.

### 5.6 Setup steps: setup-go, setup-node and setup-python

- Every non-test step together is at most ≈1.4 min on Windows and ≈1.5 min on macOS [CI, derived]. So tuning setup-go or setup-node saves well under a minute.
- setup-python runs on ubuntu only (`ci.yml:69`), so it is off the critical path.
- setup-node and the Lanterns node tests run on all three legs (`ci.yml:83-87`). The `.mjs` tests check JavaScript presentation contracts. If they read no OS paths, they could run on ubuntu only. Ranked as H8.
- golangci-lint runs on all three legs (`ci.yml:88-91`). Its findings depend on GOOS through build-tagged files, so dropping it from Windows or macOS would need `GOOS=windows` and `GOOS=darwin` lint runs on ubuntu. That is worth doing only if H1 shows lint costs more than ≈30 s on the critical legs.

## 6. Ranked changes

Ranked by critical-path saving (Windows, then macOS) after d177, then by coverage risk, then by PR size. "Coverage-reducing" marks the items the Guildmaster decides one by one. Savings are per run, in minutes. ubuntu savings do not shorten the wall time after d177 but are listed for completeness. Every saving should be confirmed on at least two runs (runner variance up to 2x).

| Rank | Change | PR size | Windows | macOS | ubuntu | Coverage risk |
|---:|---|---|---:|---:|---:|---|
| H1 | Record per-package and per-test timings in CI | S | 0 (enabler) | 0 | 0 | none |
| H2 | Quality-judge store lock honours `lock_wait`; tighten its test | XS | ≈1–2 (≈1 after H3) | ≈1–2 (≈1 after H3) | ≈1–2 | none |
| H3 | Managed step runs only the tests a managed copy changes | M | ≈2–2.5 | ≈2–2.5 | ≈1.5 | none, if the selection is complete |
| H3b | *(coverage-reducing alternative to H3)* Managed step on ubuntu only | XS | ≈4.3 | ≈4.4 | 0 | medium |
| H4 | Run `internal/cli` as concurrent shards in both test steps | S–M | ≈1–3 | ≈1–3 | ≈1–2 | none |
| H5 | Rolling Go build cache, and add `-count=1` to the test steps | S | ≈0.5–2 | ≈0.5–2 | ≈0.5–1 | none; closes today's cached-pass gap (§5.3) |
| H5b | *(coverage-reducing option on H5)* Cached test results on PRs | XS | up to ≈5 on small PRs | up to ≈4 | up to ≈3 | medium; wider than today's gap |
| H6 | Shorten the other slowest tests | M, one PR each | unknown until H1 | ≈0.5–1 | ≈0.5–1 | none to low |
| H7 | *(coverage-reducing)* Platform-neutral packages on ubuntu only | S | ≈0.5–2 | ≈0.2–0.5 | 0 | medium-high |
| H8 | *(slightly coverage-reducing)* Lanterns node tests and setup-node on ubuntu only | XS | ≈0.2–0.5 | ≈0.2–0.5 | 0 | low |
| H9 | *(slightly coverage-reducing)* Docs-only PRs skip the Go jobs | S | <0.5 averaged | <0.5 averaged | <0.5 averaged | low |

How each figure is traced:

- **H2:** 60 s [local] in each of the Test and managed steps, as long as `internal/cli` is that step's long pole. It is in the managed step on every OS and in the Test step on macOS and ubuntu at least [2026-10-22, local]. The 30 s wait is runner-independent.
- **H3:** the managed step's 2.8, 4.3 and 4.4 min [CI] times the 56% of its local test time spent outside the tests that go through `maybeManage` (§4.3), rounded down because the test binary's build stays.
- **H2 after H3:** `TestQualityJudgeConfiguredLockWait` is not a converted test, so H3 already removes it from the managed step and H2 then saves only its Test-step minute.
- **H3b:** the whole managed step [CI]. After H3, H3b's extra saving is only the selected remainder.
- **H4:** `internal/cli` ≈4.3 min on Windows and macOS [est., §4.1], cut by N shards down to its slowest test plus the binary build. Local data says the floor is the 62 s quality-judge test until H2 lands, then ≈15 s. On Windows, contention and the unknown long pole (§4.2) cap the gain at 1–3 min.
- **H5:** compile time is inside "everything else" and the Test step [CI]. There is no measurement of it, so this is a range [est.]. Adding `-count=1` costs only the cached passes CI gets today, which at `2f7d34f8` are small leaf packages under 4 s each [local, est., §5.3]. That cost grows for a while after each `go.sum` change.
- **H5b:** the Test step minus the packages a PR changes and the packages that import them [est.]. A PR that touches `internal/cli` still pays the long pole. The baseline is today's snapshot cache, which already reuses a few leaf packages, so H5b's extra saving is the difference between a cache refreshed on every main push and one refreshed only when `go.sum` changes.
- **H6:** per test, §4.2 and §5.5 [local]. The Windows effect is unknown until H1.
- **H7:** the named packages' share of package time on 2026-10-22 Windows [2026-10-22]. They are not the long pole, so the gain comes only from less contention.
- **H8:** at most the part of "everything else" these steps cost [CI, derived, est.].
- **H9:** 3.5% docs-only (§4.4) × ≈12 min [est.].

### H1. Record per-package and per-test timings in every CI run

Run the Test and managed steps with `go test -json` saved to a file. A small summariser in `.github/tests/` (Go or Python, with no new dependency) writes the 20 slowest packages and the 20 slowest tests to `$GITHUB_STEP_SUMMARY` and uploads the JSON as an artifact. The human-readable failure output must stay as readable as today, for example by also printing failures from the JSON. This gives the Windows and macOS tables this report could not fill in. H4, H6 and H7 should be re-sized from them. Size S, no coverage change.

### H2. The quality commands honour `lock_wait` on the store; tighten the test

Find which store open in `quality judge` and `quality judged` uses `config.DefaultLockWait` instead of the copy's `lock_wait`, and pass the configured wait through. In `TestQualityJudgeConfiguredLockWait`, lower the ceiling below the 30 s default, as `TestAdvancePreflightLockWait` does, so a regression fails. First confirm the 30 s on CI with H1's data or with a run of the one test. Expected saving: ≈1 min in each of the two `internal/cli` runs on every leg. Size XS, no coverage loss. It may also be a user-visible fix: a configured `lock_wait` that is ignored.

### H3. The managed step runs only the tests a managed copy changes

The rule is that every Director suite that loads config runs both ways through one shared helper (`docs/director-modes.md:154`), not that the whole package runs twice. Only `managedRun()` (`internal/cli/dual_mode_test.go:26-31`) reads the mode. Its callers are `maybeManage` (lines 34-40) and `newRunDirectorAt` (`advance_test.go:88,110`). `maybeManage` is called from `newCopy` (`copy_test.go:17-26`), `directorCopy` (`director_test.go:40-87`), `newRunDirectorAt` (`advance_test.go:85-112`) and once directly (`advance_test.go:346`).

Design: compute the selection from the tests that reach `maybeManage`, not from file names. A test can reach it through another helper, so a file-name list goes stale. For example:

- a run records each test in which `managedRun()` was asked, not only each test that reached `maybeManage`, to a list (§4.3 measured with the narrower `maybeManage` hook: 191 tests);
- the managed step runs `-run '^(…)$'` over that list;
- a guard fails the build if a managed run converts a copy in a test that is not on the list.

Optionally (§5.2), the managed process can run alongside the Test step instead of after it. Keep `GUILDHALL_CI_REQUIRE` on it. Expected saving: §4.3 and the table. Size M, no coverage loss if the selection is complete.

### H3b. *(Coverage-reducing alternative)* Managed step on ubuntu only

Add `if: matrix.os == 'ubuntu-latest'` to the managed step (`ci.yml:63`). It saves the whole step on Windows (≈4.3 min) and macOS (≈4.4 min), the largest single cut available. Risk: medium. `manageCopy` (`dual_mode_test.go:47-80`) rewrites `config.toml` and `managed.toml` and the store, including a `\r?\n` line match (line 62). Windows path and line-ending handling of managed copies would then be proven only on Linux and macOS, and not at all on Windows. If H3 is done first, H3b's extra saving is only H3's remainder.

### H4. Run `internal/cli` as concurrent shards

`internal/cli`'s tests call `t.Setenv` and `os.Chdir`, so `t.Parallel` is not a mechanical change. Shard by process instead:

- `go test -list . ./internal/cli` lists the tests;
- the names split into N groups by a stable hash, or by H1's timings;
- N `go test ./internal/cli -run '^(…)$'` processes run at once, beside `go test` over every other package.

Each shard is its own process, so `Setenv` and `Chdir` stay safe. Every shard keeps `GUILDHALL_CI_REQUIRE` and `TestMain`'s guards (`docs/contracts/testenv.md:30-31`). A guard checks that the union of the shards equals the `-list` output, so no test is dropped. The same split applies to the managed step. Windows time is mostly waiting on fsync and process starts (`windows-ci-speed.md:13`), so concurrency there helps more than the CPU count suggests. After H2, the local floor is the ≈15 s `TestAdvancePreflightLockWait` plus the binary build. Size S–M, no coverage loss.

### H5. A rolling Go build cache, with `-count=1` added to the test steps

Add `-count=1` to the Test, managed and race steps (`ci.yml:60,67,82`), which run without it today (§5.3), so no step reuses a cached test pass. This half closes the existing gap and can land alone as an XS PR. Its cost is the cached passes CI gets now, small at `2f7d34f8`.

Then turn off setup-go's cache (`cache: false`) and add `actions/cache` for `GOCACHE` and the module cache. Key it on OS, Go version and `github.sha`, with `restore-keys` on OS and Go version. Main pushes refresh it, and PRs restore the newest entry, so build, vet and test-binary compiles start warm. First measure restore and save time on Windows, where tar extraction is slow. The repository's cache is limited to 10 GB, and the oldest entries are evicted first. Do not land the rolling cache without `-count=1`: a cache refreshed on every main push would make most unchanged packages report `(cached)`, which is H5b without its decision. Size S. No coverage loss, and it removes today's coverage gap.

### H5b. *(Coverage-reducing option)* Cached test results on PRs

On top of H5's rolling cache, add `-count=1` only on main pushes, so PR runs reuse cached passes for packages that neither change nor import anything that changed. Every merge is still fully proved on main. Today's snapshot cache already reuses passes, but only for packages untouched since the last `go.sum` change (§5.3). H5b makes that reuse routine and fresh. It saves the most on PRs that change few packages, and little on PRs that touch `internal/cli` or the widely imported `internal/testenv`, `internal/config` and `internal/store`. Risk: medium, and wider than today's gap. A cached pass is not re-proved against a new runner image, git version or PowerShell, and a required-skip outcome under `GUILDHALL_CI_REQUIRE` is reused as it was. Size XS.

### H6. Shorten the other slowest tests

One PR per test, picked from H1's data. Local candidates (§5.5):

- the stop-ladder contracts (≈40–64 s each in `internal/artisan` and `internal/launch`); shorter rungs for the fake harness would need `docs/test-timing.md:24` amended;
- `TestPaneOperationsTimeoutRetainsClaimWithoutRetryOrStop` (≈50 s, Unix only), whose five phases could run as parallel subtests;
- `TestRunCleanupReportsAnOrphanedWorker` and `TestSessionEndReportsASurvivingProcess` (≈10 s each).

Earlier fixes that worked were shared fixtures and fewer process starts (`windows-ci-speed.md:28-30`). Size M per test, no coverage loss if the observed behaviour stays the same.

### H7. *(Coverage-reducing)* Platform-neutral packages on ubuntu only

Windows and macOS would run `go test` over the package list minus the packages that have no OS-specific files and no `runtime.GOOS`, computed in the step so the list stays current. Expected saving: ≈0.5–2 min on Windows, from less contention only. Risk: medium-high. It drops Windows and macOS proof for exactly the packages whose fsync, git, lock and path behaviour the Windows leg checks. It also needs `windows-ci-speed.md` and `ci-matrix-and-lint.md` amended by a decision record. Size S.

### H8. *(Slightly coverage-reducing)* Lanterns node tests and setup-node on ubuntu only

Add `if: matrix.os == 'ubuntu-latest'` to the setup-node and Lanterns steps (`ci.yml:83`, `ci.yml:86`). The task that takes this up should first check that the `.mjs` tests read no OS paths. Expected ≈0.2–0.5 min on Windows and macOS. Size XS.

### H9. *(Slightly coverage-reducing)* Docs-only PRs skip the Go jobs

A first step in the `test` job decides whether the PR changed only `docs/` and `*.md`. If so, the Go steps other than `go test ./internal/doccheck/...` are skipped, and the job still succeeds, so the three required checks report. Do not use a workflow-level `paths-ignore`, which would leave the required checks pending. Expected saving under 0.5 min per run when averaged across all PRs. Size S. Lowest value of the list.

## 7. What every follow-up must keep

- **Required check names.** The three required checks are the `test (<os>)` jobs. A new or renamed job needs a branch-protection edit (`ci-action-pins-and-race.md:36`). Every change above stays inside the existing `test` job.
- **`GUILDHALL_CI_REQUIRE`** must stay on every step that runs the tests it governs, including shards and managed subsets (`docs/contracts/testenv.md:5`). Otherwise a required skip turns back into a silent skip.
- **The timing-policy step** (`ci.yml:51-52`) and the claude and codex guards in `TestMain` (`docs/contracts/testenv.md:30-31`) must still run in every shard and subset.
- **Cached test passes.** The test steps run without `-count=1` today (§5.3). A follow-up that adds a rolling cache must add `-count=1` with it, unless the Guildmaster has chosen H5b.
- **`go test`'s ten-minute package timeout.** Shards make it less likely to bite, but a slow Windows runner can still reach it, which is why `Ceiling` is capped at two minutes (`docs/contracts/testenv.md:19-20`).
- **Runner variance of up to 2x** (`windows-ci-speed-2026-10-22-timings.md:60`). Confirm each saving on at least two runs, ideally of the same commit.
- **d177.** The race split changes only the ubuntu leg. If d177 moves race to a parallel job, H5's ubuntu cache helps that job too. H2 and H4 shorten the race run's `internal/cli` as well, because `-race` runs the same tests.
- **Coverage-reducing items** (H3b, H5b, H7, and to a lesser degree H8 and H9) each need the Guildmaster's separate decision. H7 also needs a decision record amending `windows-ci-speed.md:34`.
