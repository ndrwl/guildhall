# `internal/adapter/runtimes`

## Contract

The one owner of the runtimes Guildhall can launch ([running an attempt](../adapters.md#running-an-attempt)).
`Names` lists them in sorted order, `claude` and `fake`; `Lookup` returns the runtime called a name, its commands running the given guildhall executable, and refuses any other name, including the interpreter's test-only `mock`, with an error wrapping `ErrUnknown` that names it and lists `Names`.
It is keyed by `adapter.FakeName` and `adapter.ClaudeName`, the names' one spelling, which `interp.FakeAdapter` and `interp.ClaudeAdapter` also refer to.
`Runtime` is the consumer's narrow view of an adapter: its attempt command, refused with no command for an invalid spec, and `CheckSettings`, which refuses a model or effort the runtime cannot honour with an error wrapping `adapter.ErrUnsupportedSetting`, honours the zero `adapter.Settings` and starts nothing, so a preflight runs it before any attempt is recorded; `Command` refuses what it refuses.
`Session` is the supervisor's narrow view of a runtime's Director session: the program it needs on `PATH` (none for the fake, which is the guildhall executable), its skills directory, its own session files, its session command, whether it has turn hooks signalling each turn's start and end, `TurnEndHook`, and whether it reports its context use with `guildhall director context-report`, `ContextReport`, an optional capability; `LookupSession` returns it for every name `Names` lists and refuses any other as `Lookup` does.
`PersistentSession` is the optional filesystem/trust boundary for a runtime that needs a stable interactive folder identity: `PrepareSessionWorkspace` publishes rendered runtime files only after the previous process ends, returns the stable working directory or an error that starts no process, and never changes the numbered record or runtime-owned trust state; `SessionTrust` is a read-only advisory notice, never authorization.
The Director supervisor exercises it over Claude's real workspace and trust-gated process stub in `TestDirectorClaudeReusesTrustedWorkspace`, `TestDirectorClaudeReloadReusesTrustedWorkspace` and `TestDirectorClaudePreflightNamesTrust` ([red evidence](../evidence/director-trust-2026-10-23-red.txt)).
`artisan start` is the consumer, in both of its modes; `launch.Process` passes names through to it, and the interpreter's test-binary guard, `interp.CheckAdapter`, is unchanged.

## Tests

`runtimes_test.go` was written red first against stub bodies ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): each name builds its adapter's subcommand over the executable in the spec's working directory, with no command for an invalid spec, and unknown names are refused by name with the list.
`TestEachRuntimeChecksSettings` was written red first ([red run](../evidence/profile-model-effort-2026-10-23-red.txt)): every runtime honours the defaults and its `Command` refuses what its `CheckSettings` refuses, and Claude refuses an effort Claude Code does not take, naming it.
`TestLookupSessionConstructsEachRuntime` pins `LookupSession` the same way; it was added with the implementation and was not run red.
