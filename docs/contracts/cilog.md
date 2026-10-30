# `internal/cilog`

## Contract

The CI log cache and the log excerpt of [failed job logs](../interpreter.md#validate) ([decision](../decisions/failed-job-logs.md)).
Validate and [`guildhall ci log`](../ci-logs.md) both read a failed job's log through `Get`, so each log is fetched once while its cache stands.

`Dir(state, repo, task)` is the cache, `<state>/<repo>/<task>/ci-logs`; each log is `<job>.log` in it.
The cache is disposable: it holds no store document, `guildhall task files` never lists it, and deleting it at any time only makes the next `Get` fetch again.

`Get(ctx, reader, dir, commit, job, limit)` returns the cached log when its file exists, with no forge call, and otherwise fetches it with `reader.CheckLog` (`Reader`, the method of `interp.CheckLogReader`), writes it and returns it.
It returns the log's file, its text and the bytes dropped from its head.
A log cut short is cached with the first line `[guildhall: <N> bytes dropped from the head of this log]`, which `Get` reads back as `Dropped` and leaves out of the text.
The file is written to a temporary file and renamed into place with `atomicfile`, so a concurrent reader sees the whole log or none, and a failed fetch leaves no file.
A job that is not all digits (`ValidJob`), which keeps it a file name in the cache, a limit of zero or less, and no reader on a cache miss are `forge.ErrLogUnavailable` with the reasons "not a GitHub Actions job", "log fetching is off" and "forge does not provide logs", without reading the forge.
A fetch error is returned as the reader gave it.

`Excerpt(text)` is the log excerpt: the step of the log holding its first `##[error]` line, from the last `##[group]Run` line before it up to the next `##[group]Run` line, or the whole log when it has no error; of that, the last `ExcerptLines` (200) lines without their leading ISO timestamps, the log's byte order mark, CRs and ANSI escape sequences being removed before any line is matched, trimmed by whole lines from the head to at most `ExcerptBytes` (32 KiB), and a single longer line cut to its tail at a rune boundary.
A leading byte order mark and trailing carriage returns are dropped.
It depends on the text alone, so a replay computes the same excerpt from the same cached log.

One implementation and no interface beyond `Reader`.
Platform differences: none in the contract.

## Tests

`TestExcerptGolden` pins the excerpt of four fixture logs, two recorded in `testdata` (an error in a middle step and no error) and two built by the test (a step of more than 200 lines and a step over 32 KiB), against golden `.excerpt` files that `-update` rewrites; `TestExcerptBounds` pins the empty log and one line longer than the cap.
`TestGet` pins a miss fetching and caching, a hit with no forge call, a pruned cache fetched again, and a truncated log's header in the file and its dropped count read back; `TestGetUnavailable` pins that a failed fetch leaves no file and the unavailable reasons without a forge call.
These tests were written with the package, not red first.
