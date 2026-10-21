# `internal/steward/stewardtest`

## Contract

The shared transport contract suite, `RunCopierContract` with its `Harness`, that every `steward.Copier` must pass, and `Fleet`, an in-memory `Copier` for testing rollout consumers without scp, ssh or a network.
`Fleet` holds each machine's files as a map for its own lifetime; `Refuse` makes every later copy to or from a machine fail, `Hang` makes it wait until its context ends, and `Calls` returns and forgets the copies attempted so far; it has no directories, permissions or partial transfers.

## Tests

`fleet_test.go` runs the suite against `Fleet`; `internal/steward/scp_test.go` runs it against the scp `Transport`.
`Fleet` is the double in `internal/cli`'s steward tests.
