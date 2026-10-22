# `cmd/guildhall`

## Contract

The process entry point: arguments, standard streams and exit status are handed to `cli.App.Run`.

## Tests

`main_test.go` builds the binary with the release version linker flag and runs `version`, `doctor` and an unknown command, then renders through the release skills and, for a request with no baseline, the [release baseline](baseline.md).
`run_test.go`, `TestTaskRunsEndToEnd`, is the S7 acceptance on every CI runner: the built binary approves the repository's fake-straight workflow, which has no validate step and so is below the review floor, then runs task app/t-1 on `internal/cli/testdata/run` from `task new` to its merge decision through `task start`, the fake Artisan in owned launcher processes, `step done`, `decide answer` and `task start --decision`, proves every launcher stopped, and asserts the timeline's attempts, decisions and event order.
It was written after the commands and passed on its first run.
`director_safety_test.go` holds the Director [safety checks](../director.md#safety-checks): approval, guard rails, crash recovery and zero idle turns, each on the fake Director and the Claude stub side by side, over one build of the binary shared by every check.
Its `TestMain` makes the test binary the Claude stub when started as `claude`.
The checks join slices already merged and passed on their first run; each was then run against a binary with its safety behaviour removed and failed ([mutation runs](../evidence/director-safety-checks-2026-10-22.txt)).
Each binary test points its copy of the run config at a real repository and local bare remote built with git (`gittest.Repository`), so the binary makes the task's working directory a real worktree; `TestTaskRunsEndToEnd` then asserts that worktree is on branch `guildhall/t-1` at the remote's default branch and listed by the repository.
Those assertions were added with worktree custody and passed on their first run; before the configs were pointed at a repository, every binary test failed at `task start` with git unable to enter `/srv/app`.
`TestTaskRunsEndToEndWithTheWatcher` is the same run from a Director copy without typing `step done`: each attempt's launcher, the per-task [watcher](../running.md#the-watcher), takes its result and opens the next decision, which the test waits for in the store before answering it.
`TestTheWatcherTellsTheDirectorOfAKilledWorker` kills the plan attempt's worker, which never publishes a result, and reads the `attempt-failed` Director message the launcher's `task start` sent; nobody types `task start` until the test checks that the task waits for the Director.
Both were written red first ([red run](../evidence/launcher-watcher-2026-10-22-red.txt)) and timed out waiting for the decision and the message.
`TestTheWatcherTellsTheDirectorOfAFailedAdvance` holds the task lock in the test while the plan attempt ends and past both of its launcher's advances, reads the `advance-failed` message naming `step done` and the lock error, finds the attempt first in `director status`'s `needs_me` as an `advance` item with its `step done`, and after releasing the lock recovers with `task start`, which opens `accept-1`.
It was written red first ([red run](../evidence/watcher-advance-and-wake-2026-10-22-red.txt)): after five minutes no message had been sent, the launcher having logged one lock timeout.
`TestRetryAfterAKilledLauncher` runs the plan stage with a fake that never publishes a result, kills its launcher outright, settles the attempt `failed` with `artisan resume`, sees `task start` wait for the Director, and retries with `step retry --reason`, which starts attempt 2; repeating it changes nothing, and the timeline records the retry message after attempt 2 starts.
It was written red first ([red run](../evidence/attempt-retry-2026-10-22-red.txt)) and failed at `step retry`, a usage error, after the kill, resume and wait had passed.
`TestDirectorGrillRunsEndToEnd` runs the built binary over `grill.toml` with the test playing the Director: it reads the prompt the `director-stage` message names, finishes the grill with `step finish`, and the task runs through the fake Artisans and the Guildmaster's answers to the merge decision, with the grill notes as the plan's context and the grill attempt in the timeline; it was written after the engine and CLI changes and passed on its first run.
`TestRetryWithAnUnknownAdapterSendsNoReason` stops the plan attempt, points its profile at adapter `nosuch` and retries with one reason, whose launch fails with no reason stored, then restores the profile and retries with another, which launches attempt 2 and stores that reason; it was written red first and failed because the failed retry had stored its reason.
