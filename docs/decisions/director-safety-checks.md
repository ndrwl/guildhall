---
status: accepted
date: 2026-10-22
topic: director
amended-by: director-wake-hook.md
formerly: D80, 0080-director-safety-checks.md
---

# The Director safety checks drive the built binary with stand-in runtimes

## Context

The captain asked for four scripted safety checks in CI with stand-in models: approval, guard rails, crash recovery and zero idle model calls ([safety checks](../director.md#safety-checks)).
`cmd/guildhall` and `cli` are already the slowest Windows packages.

## Decision

- **Through the binary, on both stand-ins.**
  Each check runs the built binary's `director start` as the Guildmaster would, with the role prompt scripting what the Director types.
  The fake Director and the Claude stub play the same script through `fake.RunScript`, which `adaptertest.RunSessionContract` already holds both to, so a check that passes means the same on either runtime.
- **A model call is a turn**: the first message, and each line delivered to the runtime.
  `fake.RunScript` appends a line per turn to the file `GUILDHALL_SESSION_TURNS` names, so both stand-ins count the same way.
- **Slow runners pay once.**
  Both runtimes run side by side within a check and the four checks run in parallel, so the 30 s idle hold is paid once; the binary is built once for every check; every wait is on a store record, a file or a process with a three-minute ceiling.
- **A stand-in Herdr.**
  The zero-idle check binds its session to a pane with `director start`'s `--herdr-session`, `--herdr-workspace`, `--herdr-tab` and `--herdr-pane`, and the test binary also stands in as `herdr` on PATH, as it does as `claude`, holding the bound tab and recording each line `pane run` types.
  For the fake runtime the check plays the pane's terminal, handing the typed line to the session's standard input as one turn; for Claude it asserts that nothing is typed into the pane and that the rewake hook delivers exactly one turn ([wake](director-wake-hook.md)).

## Alternatives rejected

- Counting the stub's process starts: it misses turns inside one long session, which is what an idle Director polling would cost.
- `adaptertest.ShellPanes` or the tab tests' Windows double: they serve an in-process `TabHost` the built binary cannot use, and `ShellPanes` ignores a line typed while a program runs, which is the case the check is about.

## Consequences

The typed-wake check was written red first ([red run](../evidence/director-safety-typed-wake-2026-10-22-red.txt)); since the [rewake hook](director-wake-hook.md) only the fake runtime is typed into.
The managed `managed.toml` row waits for managed mode, which the captain deferred.
The checks run the repository's local workflow without validate, because approval needs a workflow below the review floor and the crash and idle checks need a fake Artisan the test controls.
The shipped default workflow's validate and merge rows run over `forgetest.Fake` in `internal/interp`'s `TestDefaultWorkflowDelivers` instead; the built binary has no forge seam and would need `gh`, so the crash recovery check stops at the deliver step's merge decision.
