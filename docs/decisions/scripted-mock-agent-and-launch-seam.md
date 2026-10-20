# Scripted mock agent and the launch seam

The interpreter declares its own narrow launch interface, `interp.Launcher`, rather than consuming the adapter package directly, because it is the seam the interpreter's tests replace and it carries the rendered manifest the adapters never see ([scripted mock agent](../mock-agent.md)).
`interp.Spec` names the adapter explicitly, so a launcher can refuse an adapter it does not run and the mock can refuse anything but itself.
The mock agent is one scripted double in `interptest`, not a separate framework: it checks what it is given against an ordered script and returns scripted responses, and it claims nothing about semantic behaviour.
The no-real-adapter guard keys on `testing.Testing()`, which every Go test binary sets, so it needs no flag that a CI configuration could forget.
The [script refusal semantics](../mock-agent.md#scripts) keep one matching mistake from being reported repeatedly.
The blocked question file is provisional and owned by `internal/interp` until result intake defines how the interpreter reads it.
