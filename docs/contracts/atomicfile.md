# `internal/atomicfile`

## Contract

`Write(path, data)` replaces `path` by writing a temporary file beside it (mode 0600, not synced) and renaming it over `path`; on failure the temporary file is removed and the earlier file is unchanged.
`Open(path)` and `Read(path)` open and read a file that may be republished at the same instant, so a reader sees no file or one whole version.
An open or rename that fails for a transient platform reason is retried every 10 ms for up to 250 ms and never decides anything: on Windows the transient errors are `ERROR_SHARING_VIOLATION` and `ERROR_ACCESS_DENIED`, which another process's rename or open of the same name returns; elsewhere none is, because a rename there never races an open.
A missing file (`fs.ErrNotExist`) and every other error are returned at once, and a transient error that outlasts the bound is returned as it is.
A reader that closes between reads leaves a republish its bound; a test that never closes is testing the platform, not this package.
One implementation and no interface, like `digest` and `strictjson`.

Consumers: `artisan`'s containment and heartbeat writers and heartbeat reader; `adapter.ReadResult`, so `WaitResult` does too; `interp`'s attempt-file writer; `artisan.ReadOutcome`; `launch.Files.Result`; `cilog`'s CI log cache and the file `guildhall ci log --out` writes; and the test helpers of `artisantest` and `interptest`, including `artisantest.Files.ValidResult`.
The retry policy does not cover cancel-marker checks.
`outcome.json` and adapter result files are still published once with a hard link (`os.Link`), which never replaces a file.
`interp`'s prepared attempt directory is published by renaming a directory, which this package does not cover.

## Tests

The retry-bound and immediate-missing-file assertions now run with `testing/synctest`, not wall-clock duration limits.
The exact-deadline retry count includes the initial call and the call at the deadline before the next interval exceeds it.
Windows exclusive-handle tests retain real file handles but advance release and retry on the same synthetic clock ([timing policy](../test-timing.md)).

`retry_test.go` was written red first against a stub that did not retry: a race is retried until success, a permanent error or a race followed by one returns at once, and a lasting race is returned after the bound.
`TestReadDuringWrite` runs 128 concurrent read/write pairs on every runner, joining each pair before starting the next so a Windows rename faces only one bounded read; every read and write succeeds and each read sees a whole version, without a machine-speed-dependent throughput requirement.
On Windows, `TestReadWaitsForExclusiveHolder` holds the file without sharing for 50 ms and `TestWriteWaitsForReader` holds it open for reading, and `TestTransientWindows` pins the two errors.
`artisantest.RunStopContract`'s Windows case, which replaced `internal/artisan`'s `TestResultHeldAtDecisionIsFinished`, holds a published result without sharing while the launcher decides and requires `finished` with no error, the failure `TestStopContract`'s L16 hit on `main`; it runs for every `AttemptFiles` adapter; `TestStopContract`, `TestFakeStopContract` and `TestWaitResult*` pass unchanged.
