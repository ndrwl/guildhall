# `internal/testresults`

## Contract

The reader of a `go test -json` (test2json) stream, as CI's [test timings](../decisions/ci-test-timings.md) upload it in each `test-timings-*` artifact; [`forge.GitHub.TestResults`](forge.md) summarises each stream it downloads through it.
It uses only the standard library, since `.github/tests/timings` is a `main` package that cannot be imported.

`Parse(r, limit)` reads the stream one line at a time into a `Summary`, with memory bounded by the stream's packages, top-level tests and skips, not by its output:

- `Packages`, sorted by import path, each with its `State` and `Elapsed` seconds from the package's result event: `pass`, `fail` (a failed test, a timeout or a failed test binary), `build failed` (a result carrying `FailedBuild`, or a failing package whose own `build-fail` event, keyed by `ImportPath` rather than `Package`, came first; a dependency's `build-fail` adds no package), `no tests` (a package-level `skip`, which go test gives a package with no test files and which is never a skipped test), and `no result` (a package the stream started but never ended, as a job killed mid-stream leaves it);
- `Tests`, every top-level test that passed or failed, with its package, whether it failed and its elapsed seconds, in the order they ended; subtests (names holding `/`) and skipped tests are never ranked, as `.github/tests/timings` does not rank them, and a test still running when its package ended, as at a timeout, has no entry;
- `Skips`, every skipped test or subtest, in order, with the first line of its reason: the last line the test printed that starts like a `t.Log` header (`file_test.go:12: `), else its first line of output, without indentation and test2json framing, cut to `MaxReason` (256) bytes at a rune boundary, or empty when it printed nothing.

A line that is not a JSON event, such as a warning printed outside test2json, a line with CRLF ends or a last line cut short, is ignored, and so is a line over 1 MiB, without being held.
A stream longer than `limit` bytes is a `*TooLargeError` ("larger than N bytes", matching `ErrTooLarge`) and a read error is returned as it is, each with no partial summary.
`Summary.Slowest(n)` returns at most `n` top-level tests, slowest first, ties by package and then name, without reordering the summary; `Summary.Count(state)` counts the packages in a state.

One implementation, no interface. Platform differences: none in the contract.

## Tests

`testresults_test.go` pins, over recorded streams in `testdata` copied from `.github/tests/timings/testdata` (pass, fail, build failure, timeout) and one recorded with go 1.27 for skips: each package's state and elapsed time, `no tests` kept apart from skips, top-level-only timings, a timed-out test with no timing, skip reasons from a log line, a multi-line skip message and a bare skip; non-JSON lines, CRLF ends and a truncated last line ignored, a stream cut mid-package giving `no result`, a line over 1 MiB skipped, and a `build-fail` keyed by `ImportPath` without `FailedBuild`; the stream cap at and one byte over the limit, and a read error; a reason's first output line and its cut at a rune boundary; and the ranking and its bound.
These tests were written with the package, not red first.
