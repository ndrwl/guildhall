# `internal/adapter/fake`

## Contract

The fake runtime described under [Fake adapter](../adapters.md#fake-adapter): `Adapter.Command` loads the spec's copy/profile reference through `adapter.ProfileTools` and refuses a profile granting allowed tools, since the fake grants none; an undeclared profile is also refused.
`CheckSettings` honours every model and effort; `Command` passes `Spec.Settings` as `--model` and `--effort` after `--result`, each only when set, `ParseArgs` reads them into `Args.Settings`, and a worker (not a grandchild) records them, the zero `adapter.Settings` included, as `SettingsFile`, `fake-settings.json` beside the result file, before it reads its prompt; `RecordedSettings` reads it back, `fs.ErrNotExist` before the worker ran.
`Main` is `guildhall fake-artisan`; `Run` handles interrupts itself, honours the attempt's cancel marker unless told to ignore it, and with the P0 lifecycle directives plays a process tree (`grandchildren`, `grandchild-cmd`), ignores the marker or interrupts, publishes a late result, echoes input, bounds its lifetime and records every process's identity in an inventory directory through `artisan.RecordProcess`.
Once its work ends, before any result, `Run` writes `ReportedUsage`, fixed measured tokens and an estimated cost, as the attempt's [usage file](../adapters.md#usage-file); work ended by an interrupt or the cancel marker writes none, and a failure to write it is shown as progress and does not stop the result.

The fake Director session: `Adapter.Session` is `guildhall fake-director --role <role> [--model <model>] [--effort <effort>] <first message>` in the session directory with the session environment, needing no program on `PATH`, skills under `skills`, no session files, no turn-end hook and no context report, so a fake session's context trigger never fires; `DirectorMain` records both settings, empty ones included, as `SettingsFile` beside the role prompt before playing its `fake-director:` lines with `RunScript`; record failure exits 2.
`RunScript` is the scripted session both session doubles share: `guildhall <args>` runs guildhall from `PATH` and goes on past a failure, `/exit` ends with 0 and `exit <n>` with n; an unknown step is exit 2 before anything runs; with no ending step it reads its input until a `/exit` line or the end.
With `TurnLogEnv` naming a file, it appends one line to it per turn, `<pid> start` for its first message and `<pid> input` for each non-blank line typed to it but the ending `/exit`, the count a test reads as the session's model calls.

## Tests

Cancel-sidecar and late-result tests wait for the existing sleeper boundary to acknowledge that work began, rather than sleeping to guess readiness.
Lifetime and ignore-cancel checks inspect the duration requested through the injected sleeper instead of asserting elapsed wall time.
These replace timing-dependent assertions without changing fake Artisan behavior ([timing policy](../test-timing.md)).

`TestFakeDirectorSessionCommand`'s model and effort flags were run red before the effort-default implementation; the CLI integration tests read the recorded settings from a real fake Director child process.

`fake_test.go`, `conformance_test.go` (the shipped fake as a real child process, on interpreter-rendered attempts mapped through `interp.Spec.AdapterSpec`, and inside the Artisan launcher on this platform's unit with `launch.Files`, where a stop must end at rung 1, the cancel marker), `conformance_unix_test.go` (typed into a tab by `adapter.TabLauncher` over `adaptertest.ShellPanes` and stopped by Ctrl+C).
`TestSettingsReachTheFake` (`settings_test.go`) was written red first against stub bodies ([red run](../evidence/profile-model-effort-2026-10-23-red.txt)).
`directives_test.go` and `directives_unix_test.go` were written red first against the fake without the P0 directives ([red run](../evidence/launcher-resume-2026-10-21-red.txt)): directive validation, the marker ending work without a result, `ignore-cancel` working on into the adapter's refusal, a late result after the marker, `result=never` ending at the lifetime, the inventory record, fake grandchildren recording and honouring the marker, native grandchildren outliving it, interactive echo, and SIGINT with and without `ignore-interrupt`.
Seven refused prompts in the validation test pass against the old fake as well, which refused every unknown key.
`TestStopContractFakeArtisan` in `internal/launch` runs the stop contract with the fake as the worker.
`director_test.go` runs `adaptertest.RunSessionContract` against the fake Director as a real child process and pins its command and `RunScript`'s refusals; the fake Director was written before these tests, which passed on first run.
`TestRunScriptCountsTurns` was written red first ([red run](../evidence/director-safety-checks-2026-10-22.txt)) with `TurnLogEnv` declared and no turn recorded: the start and the typed line are recorded with the process ID, a blank line and the ending `/exit` are not, and without the variable nothing is.
`TestRunOutputDirective` was written red first (the directive was unknown): the `output` directive writes its file before the result and lists it, and paths that are absolute, escaping, empty, backslashed or without text are refused under the [directive validation contract](../adapters.md#fake-adapter).
`TestRunReportsFixedUsage` was written red first ([red run](../evidence/attempt-spend-2026-10-22-red.txt)): a succeeded, failed, result-less and failing-exit worker each write `ReportedUsage`, and an interrupted one writes no usage file.
`TestRunWritesResultDespiteUsageFailure` was added when review found a usage write failure stopping the result.
`TestAdapterDeclaresNoCapabilitiesAndBuildsArgv`'s refusal of a spec naming allowed tools was written red first ([red run](../evidence/artisan-allow-rules-2026-10-22-red.txt)).
