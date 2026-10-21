# `internal/herdr`

## Contract

`Client` names an explicit non-default session on every call and operates only on tabs it created (`docs/adapters.md`); its process boundary, `Client.run`, passes argv and env exactly, keeps stdout and stderr apart, reports a nonzero exit with its output, errors when a program cannot start, and reports cancellation as the context's error.
`ShellCommand` renders argv so the named shell starts exactly that argv, or refuses (D20).

## Tests

`runner_test.go` (shared contract test, real `execRun` and `fakeRunner`), `herdr_test.go` (client against recorded Herdr 0.9.5 responses), `shell_test.go`, `shell_exec_test.go` (round trips executed in each installed shell; a missing shell the runner requires fails)
