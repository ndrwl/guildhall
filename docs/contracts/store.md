# `internal/store`

## Contract

`Open`, locking, mutation ids and receipts, and the records, messages, inboxes and workflow approvals (`docs/store.md`, D9, D10, D15, D22), including typed decisions bound to a subject with option-checked answers (D23), and receipts naming their record with `TaskHistory`'s one-transaction task read (D24).

## Tests

Real bbolt files: `store_test.go`, `records_test.go`, `messages_test.go`, `approvals_test.go`, `history_test.go`, `process_test.go` (separate processes), `fixture_test.go` (schema v1 file)
