# `internal/messaging`

## Contract

`Decode` and the envelope (`docs/messaging.md`), read with [`strictjson`](strictjson.md); `Mailbox`: durable send with receipts, retry and id-reuse conflict, recipient, task and attempt checks, bounded ordered listing and acknowledgement, concurrent use (D15).
`StoreMailbox` implements it on the store.

## Tests

`envelope_test.go` (with `testdata/` encodings); `store_mailbox_test.go` runs the shared suite against real store files, closing and reopening them for restart
