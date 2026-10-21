# `internal/herdr`

## Contract

`Client` names an explicit non-default session on every call; tab ownership restrictions and the pane self-report exception are defined in its [API contract](../../internal/herdr/herdr.go).
Its process boundary, `Client.run`, passes argv and env exactly, keeps stdout and stderr apart, reports a nonzero exit with its output, errors when a program cannot start, and reports cancellation as the context's error.
`ReportAgent` reports a pane's agent with `pane report-agent`, passing the resume command after `--` and refusing, before any call, a malformed pane, source, agent or state and a resume command that starts with a path or an option (`ErrResumeArgv`).
`ShellCommand` renders argv so the named shell starts exactly that argv, or refuses (D20).

## Tests

`runner_test.go` (shared contract test, real `execRun` and `fakeRunner`), `herdr_test.go` (client against recorded Herdr 0.9.5 responses; the `ReportAgent` cases were written red first, and the malformed pane, source, agent and state refusals passed against the stub, which refused everything), `shell_test.go`, `shell_exec_test.go` (round trips executed in each installed shell; a missing shell the runner requires fails)
