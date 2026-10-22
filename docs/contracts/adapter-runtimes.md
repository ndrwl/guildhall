# `internal/adapter/runtimes`

## Contract

The one owner of the runtimes Guildhall can launch ([running an attempt](../adapters.md#running-an-attempt)).
`Names` lists them in sorted order, `claude` and `fake`; `Lookup` returns the runtime called a name, its commands running the given guildhall executable, and refuses any other name, including the interpreter's test-only `mock`, with an error wrapping `ErrUnknown` that names it and lists `Names`.
It is keyed by `adapter.FakeName` and `adapter.ClaudeName`, the names' one spelling, which `interp.FakeAdapter` and `interp.ClaudeAdapter` also refer to.
`Runtime` is the consumer's narrow view of an adapter: its attempt command, refused with no command for an invalid spec.
`Session` is the supervisor's narrow view of a runtime's Director session: the program it needs on `PATH` (none for the fake, which is the guildhall executable), its skills directory, its own session files and its session command; `LookupSession` returns it for every name `Names` lists and refuses any other as `Lookup` does.
`artisan start` is the consumer, in both of its modes; `launch.Process` passes names through to it, and the interpreter's test-binary guard, `interp.CheckAdapter`, is unchanged.

## Tests

`runtimes_test.go` was written red first against stub bodies ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): each name builds its adapter's subcommand over the executable in the spec's working directory, with no command for an invalid spec, and unknown names are refused by name with the list.
`TestLookupSessionConstructsEachRuntime` pins `LookupSession` the same way; it was added with the implementation and was not run red.
