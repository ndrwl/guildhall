# `internal/cli`

## Contract

Commands, output and exit codes (README); doctor's process boundary, `App.LookPath` and `App.Output`: a missing tool is `exec.ErrNotFound`, output is stdout and stderr combined, a failing command is an error with its output, and context cancellation stops a hung command; see D6 for doctor's timeout policy.
The `store`, `messages`, `task`, `decide`, `workflow approve` / `workflow approvals` and `steward rollout` commands, including the read-only task inspection reads `task timeline` (with findings and rulings) and `task files`, `task finding rule` and `task review --full` (`docs/store.md`, `docs/messaging.md`, `docs/tasks.md`, `docs/decisions-cli.md`, `docs/configuration.md`, `docs/steward.md`); task acceptance refuses undeclared profiles in the effective workflow before opening or writing the store; see `docs/tasks.md` for task acceptance and retry contracts.

`artisan start` without `--herdr-session`, `artisan stop`, `artisan status` and `artisan resume` are [owned attempt commands](../artisan.md#commands) over [`internal/launch`](launch.md); `artisan start` with `--herdr-session` is the [tab mode](../adapters.md#running-an-attempt).
The `artisan` verbs follow the [role grammar](../decisions/0053-cli-role-grammar.md): help lists every one, and `artisan run`, the pre-release name of `start`, is a usage error that names `artisan start`.
`artisan harness inventory|kill-owned|probe --lab` is the lab's [test tooling](../artisan.md#harness), refused without `--lab`.
`fake-artisan` is `fake.Main` and `claude-artisan` is `claude.Main`; owned `artisan start` takes `--adapter fake` or `claude`, and tab mode only `fake`.

`task start` and `step done` ([running a task](../running.md)) run one `interp.Engine` advance over a store opened per record operation, the Director config and the state directory, under the task's lock, launching through `launch.Process`; `step done` refuses a result path other than the attempt's own and a result `adapter.ReadResult` refuses before calling the engine.

`render <request.json>` reads one JSON rendering request with [`strictjson`](strictjson.md); decoding refusals are usage errors (exit code 2) before installation or rendering.
It invokes `App.InstallSkills` to install a temporary first root named `release` before optional managed and Director roots.
Before installing or rendering, it validates the step settings through `workflow.ValidateStepSettings`, rejecting malformed values, invalid or duplicate names, disallowed actors and unknown setting keys with exit code 2.
It returns the prompt manifest as JSON, retains the rendered attempt, removes the temporary root, and refuses to replace an existing attempt.
`cmd/guildhall` supplies the embedded release installer; an unconfigured installer is an error.

## Tests

`render_test.go` checks malformed settings are refused before installation or attempt creation.

`advance_test.go` runs task app/t-1 over `testdata/run` (the fake-straight workflow, whose stage guides carry the fake adapter's directives) with the test binary playing every launcher: `TestTaskStartAndStepDone` and `TestStepDoneRefusesAResultItDoesNotTake` were written red first against a stub advance and failed with it; `TestTaskStartAndStepDoneUsage` passed against the stub, whose flag checks were already written.
Each test's cleanup proves every attempt's launcher stopped with `artisan stop`.
`TestTaskUsageErrors` used `task start` as its unknown subcommand; it now uses `task frobnicate`, because `task start` is an accepted command.

`artisan_owned_test.go` was written red first against stub bodies: owned `start` exit codes and JSON for each fake outcome, `status` afterwards, usage errors and `stop` without an owner; `TestArtisanStartRunsClaudeOwned` was red against the CLI that refused `--adapter claude` ([red run](../evidence/claude-adapter-2026-10-21-red.txt)), and its usage case for an unknown adapter now names `codex`, since `claude` is accepted; `TestArtisanStartCustody` is the [custody acceptance](launch.md#tests).
After the red commit one expectation was corrected because it contradicted the reconcile contract: a finished attempt whose result reports failure classifies `finished`, not `failed`.
`artisan_resume_test.go` and `harness_test.go` were written red first against stubs ([red run](../evidence/launcher-resume-2026-10-21-red.txt)): the registered resume command's shape and that it runs through this CLI; resume refusing a live launcher; L13, a launcher killed outright whose worker tree resume sweeps, settling the attempt failed; the harness refused without `--lab`, `probe`, and `inventory` and `kill-owned` against a running launcher with a fake worker tree.
`TestArtisanRoleGrammar` in `cli_test.go` was written red first ([red run](../evidence/cli-role-grammar-2026-10-21-red.txt)): help lists `artisan start|stop|status|resume` and the harness, the `artisan` usage error names all five verbs, and `artisan run` is refused with a message naming `artisan start`.
`TestHelpNamesReadmeCommands` reads the README command list and requires the help text to name every `guildhall` command in it; its first run failed on `steward rollout` and `task ... finding rule|review`, which the help then gained.
`artisan_test.go` keeps the Herdr tab mode's tests; the direct-child tests were replaced by the owned ones.

`cli_test.go`, `tools_test.go` (shared contract test, real and double); real store files: `store_test.go`, `messages_test.go`, `task_test.go` (with `testdata/` goldens, real config and workflow files, and `task_windows_test.go` on the Windows runner), `inspect_test.go` (with `testdata/` goldens from a fixed-clock store; `TestTaskFindingRule` and `TestTaskFindingRuleRecords`, red first, check the command's refusals and retry and that a task with one finding and one ruling shows both in order with `omitted_kinds` empty, and that `store doctor` counts the ruling; `TestTaskReviewFull`, red first, checks its usage errors, an unknown task, a pending retry's original receipt, the next request after one is consumed, and both in the timeline), `decide_test.go` (with a `testdata/` golden), `workflow_test.go` (with a `testdata/` golden); `steward_test.go` (rollouts on `stewardtest.Fleet` through `App.Copier`, plus one run of scp from `PATH` in a separate process)
