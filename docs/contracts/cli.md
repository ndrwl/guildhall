# `internal/cli`

## Contract

Commands, output and exit codes (README); doctor's process boundary, `App.LookPath` and `App.Output`: a missing tool is `exec.ErrNotFound`, output is stdout and stderr combined, a failing command is an error with its output, and context cancellation stops a hung command; see D6 for doctor's timeout policy.
The `store`, `messages`, `task`, `decide`, `workflow approve` / `workflow approvals` and `steward rollout` commands, including the read-only task inspection reads `task timeline` and `task files` (`docs/store.md`, `docs/messaging.md`, `docs/tasks.md`, `docs/decisions-cli.md`, `docs/configuration.md`, `docs/steward.md`); task acceptance refuses undeclared profiles in the effective workflow before opening or writing the store; see `docs/tasks.md` for task acceptance and retry contracts.

## Tests

`cli_test.go`, `tools_test.go` (shared contract test, real and double); real store files: `store_test.go`, `messages_test.go`, `task_test.go` (with `testdata/` goldens, real config and workflow files, and `task_windows_test.go` on the Windows runner), `inspect_test.go` (with `testdata/` goldens from a fixed-clock store), `decide_test.go` (with a `testdata/` golden), `workflow_test.go` (with a `testdata/` golden); `steward_test.go` (rollouts on `stewardtest.Fleet` through `App.Copier`, plus one run of scp from `PATH` in a separate process)
