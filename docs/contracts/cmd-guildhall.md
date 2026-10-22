# `cmd/guildhall`

## Contract

The process entry point: arguments, standard streams and exit status are handed to `cli.App.Run`.

## Tests

`main_test.go` builds the binary with the release version linker flag and runs `version`, `doctor` and an unknown command, then renders through the release skills and, for a request with no baseline, the [release baseline](baseline.md).
`run_test.go`, `TestTaskRunsEndToEnd`, is the S7 acceptance on every CI runner: the built binary approves the repository's fake-straight workflow, which has no validate step and so is below the review floor, then runs task app/t-1 on `internal/cli/testdata/run` from `task new` to its merge decision through `task start`, the fake Artisan in owned launcher processes, `step done`, `decide answer` and `task start --decision`, proves every launcher stopped, and asserts the timeline's attempts, decisions and event order.
It was written after the commands and passed on its first run.
`TestRetryAfterAKilledLauncher` runs the plan stage with a fake that never publishes a result, kills its launcher outright, settles the attempt `failed` with `artisan resume`, sees `task start` wait for the Director, and retries with `step retry --reason`, which starts attempt 2; repeating it changes nothing, and the timeline records the retry message after attempt 2 starts.
It was written red first ([red run](../evidence/attempt-retry-2026-10-22-red.txt)) and failed at `step retry`, a usage error, after the kill, resume and wait had passed.
`TestRetryWithAnUnknownAdapterSendsNoReason` stops the plan attempt, points its profile at adapter `nosuch` and retries with one reason, whose launch fails with no reason stored, then restores the profile and retries with another, which launches attempt 2 and stores that reason; it was written red first and failed because the failed retry had stored its reason.
