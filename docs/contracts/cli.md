# `internal/cli`

## Contract

Commands, output and exit codes (README); doctor's process boundary, `App.LookPath` and `App.Output`: a missing tool is `exec.ErrNotFound`, output is stdout and stderr combined, a failing command is an error with its output, and context cancellation stops a hung command; see D6 for doctor's timeout policy.
The `store`, `messages`, `task`, `decide`, `workflow approve` / `workflow approvals` and `steward rollout` commands, including the read-only task inspection reads `task timeline` (with findings and rulings) and `task files`, `task finding rule` and `task review --full` (`docs/store.md`, `docs/messaging.md`, `docs/tasks.md`, `docs/decisions-cli.md`, `docs/configuration.md`, `docs/steward.md`); task acceptance refuses undeclared profiles in the effective workflow before opening or writing the store; see `docs/tasks.md` for task acceptance and retry contracts.

`render <request.json>` reads one JSON rendering request with [`strictjson`](strictjson.md); decoding refusals are usage errors (exit code 2) before installation or rendering.
It invokes `App.InstallSkills` to install a temporary first root named `release` before optional managed and Director roots.
Before installing or rendering, it validates the step settings through `workflow.ValidateStepSettings`, rejecting malformed values, invalid or duplicate names, disallowed actors and unknown setting keys with exit code 2.
It returns the prompt manifest as JSON, retains the rendered attempt, removes the temporary root, and refuses to replace an existing attempt.
`cmd/guildhall` supplies the embedded release installer; an unconfigured installer is an error.

## Tests

`render_test.go` checks malformed settings are refused before installation or attempt creation.

`cli_test.go`, `tools_test.go` (shared contract test, real and double); real store files: `store_test.go`, `messages_test.go`, `task_test.go` (with `testdata/` goldens, real config and workflow files, and `task_windows_test.go` on the Windows runner), `inspect_test.go` (with `testdata/` goldens from a fixed-clock store; `TestTaskFindingRule` and `TestTaskFindingRuleRecords`, red first, check the command's refusals and retry and that a task with one finding and one ruling shows both in order with `omitted_kinds` empty, and that `store doctor` counts the ruling; `TestTaskReviewFull`, red first, checks its usage errors, an unknown task, a pending retry's original receipt, the next request after one is consumed, and both in the timeline), `decide_test.go` (with a `testdata/` golden), `workflow_test.go` (with a `testdata/` golden); `steward_test.go` (rollouts on `stewardtest.Fleet` through `App.Copier`, plus one run of scp from `PATH` in a separate process)
