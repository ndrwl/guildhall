# D38. Attempt control protocol version 1

The attempt control endpoint's request and reply lines carry `"schema": "guildhall.attempt-control/1"` and are decoded strictly ([protocol](../artisan.md#protocol)).
`stop` and `cancel` are destructive, and a launcher and the process calling it can come from different Guildhall binaries once one outlives an update, so a request whose meaning this endpoint might not share is refused rather than guessed at.
Before any identity check or handler call, the endpoint refuses a newer version, a missing or malformed schema, a duplicate key and a field version 1 does not declare, at any depth, and `Call` refuses such a reply with `ErrEndpointProtocol`.
No unversioned client existed when this protocol was introduced; the current compatibility rules are in the [protocol reference](../artisan.md#protocol).
A later version will be served only by naming it in the endpoint; the line framing, the 4 KiB request and 1 MiB reply bounds, the exchange deadline and the exact attempt and launcher match are unchanged.
