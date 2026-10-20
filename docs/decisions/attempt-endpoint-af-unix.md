# One AF_UNIX socket per attempt as the control endpoint

Each owned stage attempt's launcher answers `status`, `stop` and `cancel` on one AF_UNIX stream socket under the Guildhall state directory, on Windows as on Unix ([control endpoint](../artisan.md#control-endpoint)).
Go's standard library binds and dials AF_UNIX sockets on Windows 10 and later, so one transport, one path rule and one test suite cover all three runners; the Windows named pipe the ownership design first proposed is dropped.
The socket is bound only after the attempt's ownership record names the launcher, so a duplicate launch is refused by the record and never by probing a socket.
Taking the record already proved any previous owner exited, so a leftover file is removed only when connecting to it is refused; a leftover that answers is an unrecorded endpoint and is refused and kept, and one that cannot be tested, such as one refusing access, is kept as unknown.
AF_UNIX paths are limited to 103 bytes on macOS and 107 on Linux and Windows, so the readable `<repository>.<task>.<step>.<attempt>.sock` name falls back to 32 hex digits of a SHA-256 of the attempt key, and a state directory too long even for that is refused rather than relocated, because the path must stay a pure function of the state directory and the attempt.
The [protocol](../artisan.md#protocol) preserves the scratch launcher's line framing but requires expected target identities to prevent stale-owner requests from invoking a replacement owner's handlers.
The endpoint does not authenticate peers: access is the state directory's, made `0700`, with the socket `0600` on Unix.
