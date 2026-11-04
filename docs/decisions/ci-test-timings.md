---
status: accepted
date: 2026-11-04
topic: ci
tasks: d194
---

# CI pipes its two `go test` steps through a timing summariser that keeps the plain log

## Context

The [CI speed evidence](../evidence/ci-speed-2026-11.md) could not read any per-package or per-test CI timing, so its follow-ups H2, H4, H6 and H7 are sized from local runs (its H1).
The `test` job's Test step runs `go test -count=1 -timeout 20m` on `./...` on Linux and on the packages that are not [platform-neutral](ci-neutral-packages-ubuntu-only.md) on Windows and macOS, and its Test Director suites managed step runs `go test -count=1 -timeout 20m ./internal/cli` on Linux only ([H3b](../director-modes.md#testing-both-ways)).

## Decision

- **Both steps add `-json` to their `go test` command, with the same packages, env and flags, and pipe it into [`.github/tests/timings`](../../.github/tests/timings/main.go)**, a standard-library Go program, since Go is on every leg and Python only on ubuntu.
  It saves every input line unchanged to `-json-out`, and at EOF appends a section headed `-title` to `$GITHUB_STEP_SUMMARY`: the run's package, cached-package and top-level test counts, then tables of the 20 slowest packages (the package result's `Elapsed`, build excluded) and the 20 slowest top-level tests with their package.
  Subtests are not ranked; they stay in the JSON.
  Packages whose result line says `(cached)`, and their replayed tests, are counted but not ranked; with the [`-count=1`](ci-go-cache.md) both steps carry, a CI run normally has none.
- **The log reads like plain `go test`.**
  It prints each package's result line, compile errors as they arrive and non-JSON lines unchanged, and holds each test's output, without the `=== RUN`/`PAUSE`/`CONT`/`NAME` framing, until that name ends.
  A failing test or subtest prints its output once, when it fails, so a leaf comes before its parent.
  A failing package then prints the output of tests that never ended, which is where `go test -json` puts a timeout panic and its running-test list, then its own output, such as `TestMain` guard messages.
  Passing tests print nothing, and nothing is printed at EOF, so the failure stays in the last 200 lines of the step that [`internal/cilog`](failed-job-logs.md) keeps.
- **It never decides pass or fail.** It exits 0 after reading all input; a bad line or an unwritable file is a `warning:` line. Under the job's `bash -eo pipefail` the step's status is `go test`'s.
- **A `Test timing summariser` step** before Test runs `go vet` and `go test -count=1` on `.github/tests/timings/*.go`, which `./...`, `go vet ./...` and golangci-lint skip, and builds `$RUNNER_TEMP/timings/summarise.exe`, a name that also runs from Windows Git Bash.
  The file-list form was checked under go 1.27, so no stand-alone `go.mod` is needed.
- **Upload.** One artifact per leg, `test-timings-<os>`, holding `test.json`, plus `managed.json` on ubuntu, the only leg that runs the managed step; `if-no-files-found: warn` covers its absence on Windows and macOS. It is uploaded with the existing upload-artifact v7 pin, `if: always()`, kept 30 days. Timings are recorded on every push and pull request.
- No job, check name or `::error::`/`::group::` line is added, so required checks and the `internal/cilog` excerpt are unchanged.

## Alternatives rejected

- Python: it is set up only on ubuntu.
- Saving the JSON to a file and printing failures from it after `go test` ends: the step log would be empty while tests run.
- Printing a failing parent's whole buffer, subtests included: a failing subtest would print twice.
- Ranking subtests: table-driven names crowd out the top-level tests that shard and budget decisions are made on.
- Writing the summary while tests run, or a separate summary job: neither adds anything for an end-of-step table.

## Consequences

The `test (ubuntu-latest)` run shows two summary sections, Test and Test Director suites managed, and the Windows and macOS runs show the Test section only; every leg keeps 30 days of raw timings, for re-sizing the CI-speed follow-ups.
The summariser's tests and recorded streams in `.github/tests/timings/testdata/` must be kept in step with `go test -json` output when the Go version changes.
Running the summariser costs one small build per leg.
