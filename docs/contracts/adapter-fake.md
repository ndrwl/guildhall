# `internal/adapter/fake`

## Contract

The test-only fake runtime: no optional capabilities, bounded prompt directives (`docs/adapters.md`).
`Main` is `guildhall fake-artisan`; `Run` handles interrupts itself, honours the attempt's cancel marker unless told to ignore it, and with the P0 lifecycle directives plays a process tree (`grandchildren`, `grandchild-cmd`), ignores the marker or interrupts, publishes a late result, echoes input, bounds its lifetime and records every process's identity in an inventory directory through `artisan.RecordProcess`.
Once its work ends, before any result, `Run` writes `ReportedUsage`, fixed measured tokens and an estimated cost, as the attempt's [usage file](../adapters.md#usage-file); work ended by an interrupt or the cancel marker writes none, and a failure to write it is shown as progress and does not stop the result.

The fake Director session: `Adapter.Session` is `guildhall fake-director --role <role> <first message>` in the session directory with the session environment, needing no program on `PATH`, skills under `skills` and no session files; `DirectorMain` plays the role prompt's `fake-director:` lines with `RunScript`.
`RunScript` is the scripted session both session doubles share: `guildhall <args>` runs guildhall from `PATH` and goes on past a failure, `/exit` ends with 0 and `exit <n>` with n; an unknown step is exit 2 before anything runs; with no ending step it reads its input until a `/exit` line or the end.

## Tests

`fake_test.go`, `conformance_test.go` (the shipped fake as a real child process, on interpreter-rendered attempts mapped through `interp.Spec.AdapterSpec`, and inside the Artisan launcher on this platform's unit with `launch.Files`, where a stop must end at rung 1, the cancel marker), `conformance_unix_test.go` (typed into a tab by `adapter.TabLauncher` over `adaptertest.ShellPanes` and stopped by Ctrl+C).
`directives_test.go` and `directives_unix_test.go` were written red first against the fake without the P0 directives ([red run](../evidence/launcher-resume-2026-10-21-red.txt)): directive validation, the marker ending work without a result, `ignore-cancel` working on into the adapter's refusal, a late result after the marker, `result=never` ending at the lifetime, the inventory record, fake grandchildren recording and honouring the marker, native grandchildren outliving it, interactive echo, and SIGINT with and without `ignore-interrupt`.
Seven refused prompts in the validation test pass against the old fake as well, which refused every unknown key.
`TestStopContractFakeArtisan` in `internal/launch` runs the stop contract with the fake as the worker.
`director_test.go` runs `adaptertest.RunSessionContract` against the fake Director as a real child process and pins its command and `RunScript`'s refusals; the fake Director was written before these tests, which passed on first run.
`TestRunOutputDirective` was written red first (the directive was unknown): the `output` directive writes its file before the result and lists it, and paths that are absolute, escaping, empty, backslashed or without text are refused before anything is written.
`TestRunReportsFixedUsage` was written red first ([red run](../evidence/attempt-spend-2026-10-22-red.txt)): a succeeded, failed, result-less and failing-exit worker each write `ReportedUsage`, and an interrupted one writes no usage file.
