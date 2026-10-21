# `internal/adapter/runtimes`

## Contract

The one owner of the runtimes Guildhall can launch ([running an attempt](../adapters.md#running-an-attempt)).
`Names` lists them in sorted order, `claude` and `fake`; `Lookup` returns the runtime called a name, its commands running the given guildhall executable, and refuses any other name, including the interpreter's test-only `mock`, with an error wrapping `ErrUnknown` that names it and lists `Names`.
`Runtime` is the consumer's narrow view of an adapter: its declared capabilities and its attempt command, refused with no command for an invalid spec.
`artisan start` is the consumer, in both of its modes; `launch.Process` passes names through to it, and the interpreter's test-binary guard, `interp.CheckAdapter`, is unchanged.

## Tests

`runtimes_test.go` was written red first against stub bodies ([red run](../evidence/stall-adapter-choice-2026-10-22-red.txt)): each name builds its adapter's subcommand over the executable in the spec's working directory, with no capabilities and no command for an invalid spec, and unknown names are refused by name with the list.
