# D80. The Director safety checks drive the built binary with stand-in runtimes

The captain asked for four scripted safety checks in CI with stand-in models: approval, guard rails, crash recovery and zero idle model calls ([safety checks](../director.md#safety-checks)).

## Through the binary, on both stand-ins

Each check runs the built binary's `director start` as the Guildmaster would, with the role prompt scripting what the Director types.
The fake Director and the Claude stub play the same script through `fake.RunScript`, which `adaptertest.RunSessionContract` already holds both to, so a check that passes means the same on either runtime.
Both runtimes run side by side within a check, and the four checks run in parallel, so the 30 s idle hold is paid once.

## Counting model calls

A model call is a turn: the first message, and each line typed into the runtime's terminal.
`fake.RunScript` appends a line per turn to the file `GUILDHALL_SESSION_TURNS` names, so both stand-ins count the same way.
Counting the stub's process starts instead would miss turns inside one long session, which is what an idle Director polling would cost.

## Slow runners

`cmd/guildhall` and `cli` are already the slowest Windows packages.
The binary is built once for every check rather than once per test, every wait is on a store record, a file or a process with a three-minute ceiling, and the only fixed wait is the 30 s hold the check is about.
The Guildmaster's setup commands are a handful of binary runs per check.

## The typed wake

The zero-idle check binds its session to a pane with `director start`'s `--herdr-session`, `--herdr-workspace`, `--herdr-tab` and `--herdr-pane`, as the line `director start --tab` types does, so the Guildmaster note's wake types into that pane.
The built binary types through the `herdr` program on PATH, so the test binary also stands in as `herdr`, as it does as `claude`, on every runner: it holds the one bound tab with the runtime in the foreground and records each line `pane run` types.
The check then plays the pane's terminal, handing that line to the session's standard input, where the stand-in runtime takes it as one turn.
`adaptertest.ShellPanes` and the tab tests' Windows double serve an in-process `TabHost`, which the built binary cannot use, and `ShellPanes` ignores a line typed while a program runs, which is the case the check is about.
It was written red first ([red run](../evidence/director-safety-typed-wake-2026-10-22-red.txt)), with the stand-in's body unbuilt: the wake was recorded queued with the stand-in's error and nothing was typed.

## What is not checked yet

- The managed `managed.toml` row waits for managed mode, which the captain deferred.
- The checks run the repository's local workflow without validate, because approval needs a workflow below the review floor and the crash and idle checks need a fake Artisan whose result the test controls.
  The shipped default workflow's validate and merge rows run over `forgetest.Fake` with `SetChecks` in `internal/interp`'s `TestDefaultWorkflowDelivers`, from task creation to a merged pull request; the built binary has no forge seam and would need `gh`, so the crash recovery check takes its task through the watcher only to the deliver step's merge decision, which it leaves unanswered.
