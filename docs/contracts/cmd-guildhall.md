# `cmd/guildhall`

## Contract

The process entry point: arguments, standard streams and exit status are handed to `cli.App.Run`.

## Tests

`main_test.go` builds the binary with the release version linker flag and runs `version`, `doctor` and an unknown command.
`run_test.go`, `TestTaskRunsEndToEnd`, is the S7 acceptance on every CI runner: the built binary approves the repository's fake-straight workflow, which has no validate step and so is below the review floor, then runs task app/t-1 on `internal/cli/testdata/run` from `task new` to its merge decision through `task start`, the fake Artisan in owned launcher processes, `step done`, `decide answer` and `task start --decision`, proves every launcher stopped, and asserts the timeline's attempts, decisions and event order.
It was written after the commands and passed on its first run.
