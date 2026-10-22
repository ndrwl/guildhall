# `internal/endpoint/endpointtest`

## Contract

`RunEndpointContract(t, newHarness)` runs the [endpoint contract](endpoint.md) against one consumer, a fresh `Harness` per case: the consumer's endpoint `Path`, its `Protocol`, its `Listen`, a `Call` as the owner and a `CallOther` naming another owner, a raw `Request` line naming the owner and a `Foreign` one naming another, and `Served`, how many requests reached the consumer's server.
The cases: the owner is served with a reply carrying the consumer's schema, and on Unix the socket is `0600`; `Close` removes the socket, idempotently, and a later call fails; a stale socket is replaced; an answering socket is refused with `ErrAnswers` and keeps answering; a regular file and a directory are refused with `ErrUnknown` and kept; an oversize request, a request naming another owner (answered with the consumer's mismatch text, and `CallOther` failing with it), and requests with a missing, unversioned, zero, older, newer or foreign schema, an unknown field, a duplicate key, trailing data or no JSON are each refused with a reply in the consumer's schema and reach no server.
`RawExchange` writes one raw request and decodes the reply line; `StaleSocket` leaves a socket file nothing listens on, as an exited owner leaves it; `Ceiling` is one exchange's ceiling, scaled by `testenv.Ceiling`.

## Tests

The suite is exercised by its three consumers: `internal/endpoint`'s test ping, `artisan.TestEndpointContract` and `session.TestEndpointContract`.
