---
status: accepted
date: 2026-10-21
topic: endpoint
formerly: D34, 0034-attempt-endpoint-af-unix.md
---

# Each attempt's control endpoint is one AF_UNIX socket, on Windows as on Unix

## Context

Each owned stage attempt's launcher must answer `status`, `stop` and `cancel` from other processes.
The ownership design first proposed a Windows named pipe beside a Unix socket.

## Decision

The launcher answers on one AF_UNIX stream socket under the Guildhall state directory, on Windows as on Unix ([control endpoint](../artisan.md#control-endpoint)).
Go's standard library binds and dials AF_UNIX sockets on Windows 10 and later, so one transport, one path rule and one test suite cover all three runners.
The socket is bound only after the attempt's [ownership record](store-attempt-ownership.md) names the launcher, so a duplicate launch is refused by the record and never by probing a socket.
Taking the record already proved any previous owner exited, so a leftover file is removed only when connecting to it is refused; a leftover that answers is an unrecorded endpoint and is refused and kept, and one that cannot be tested, such as one refusing access, is kept as unknown.
AF_UNIX paths are limited to 103 bytes on macOS and 107 on Linux and Windows, so the readable `<repository>.<task>.<step>.<attempt>.sock` name falls back to 32 hex digits of a SHA-256 of the attempt key, and a state directory too long even for that is refused.
The [protocol](endpoint-protocol-v1.md) preserves the scratch launcher's line framing but requires expected target identities, so a stale-owner request cannot invoke a replacement owner's handlers.
The endpoint does not authenticate peers: access is the state directory's, made `0700`, with the socket `0600` on Unix.

## Alternatives rejected

- A Windows named pipe: a second transport, path rule and test suite.
- Refusing a duplicate launch by probing the socket: the ownership record already decides it.
- Relocating the socket when the state directory is too long: the path must stay a pure function of the state directory and the attempt.

## Consequences

Any process that can read the state directory can stop an attempt; there is no peer authentication.
The transport now lives in the [shared endpoint contract](../contracts/endpoint.md), which the Director session's signals also use.
