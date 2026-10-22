# The Director safety checks drive the built binary with stand-in runtimes

The captain asked for four scripted safety checks in CI with stand-in models: approval, guard rails, crash recovery and zero idle model calls ([safety checks](../director.md#safety-checks)).

## Through the binary, on both stand-ins

Each check runs the built binary's `director start --foreground` as the Guildmaster would, with the role prompt scripting what the Director types.
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

## What is not checked yet

- A foreground session records no pane until the Director's Herdr tab lands, so the zero-idle check pins that a Guildmaster note records exactly one wake, queued for no pane, rather than one typed line and one turn; the typed row comes with the pane binding.
- The managed `managed.toml` row waits for managed mode, which the captain deferred.
- The checks run the repository's local workflow without validate, because approval needs a workflow below the review floor and the crash and idle checks need a fake Artisan whose result the test controls.
  The shipped default workflow's validate and merge rows run over `forgetest.Fake` with `SetChecks` in `internal/interp`'s `TestDefaultWorkflowDelivers`, from task creation to a merged pull request; the built binary has no forge seam and would need `gh`, so the crash recovery check takes its task through the watcher only to the deliver step's merge decision, which it leaves unanswered.
