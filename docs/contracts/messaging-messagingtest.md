# `internal/messaging/messagingtest`

## Contract

The shared `Mailbox` contract suite, `RunMailboxContract` and its `Harness`, and `Fake`, an in-memory mailbox for consumer tests that passes it.
The fake's restart keeps its state, so it models a durable mailbox rather than proving durability.

## Tests

`fake_test.go` runs the suite against `Fake`
