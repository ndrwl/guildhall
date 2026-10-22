# `internal/session`

## Contract

The Director session supervisor's control endpoint ([the end of a turn](../director.md#the-end-of-a-turn), [decision](../decisions/director-handoff-reload.md)).
`EndpointPath` is `supervisor.sock` in an absolute session directory, `ErrPath` for a relative directory or a path longer than `artisan.MaxEndpointPath`.
`Listen` binds an AF_UNIX stream socket there for one session number, on Windows as on Unix, removing a leftover socket nothing listens on and refusing, and keeping, one that answers or any file that is not a socket; `Close` stops it and removes the socket file, idempotently.
`SignalTurnEnded` sends one `guildhall.session-control/1` request line naming the session and waits for the reply line; nothing listening is `ErrNoSupervisor`, an endpoint for another session `ErrMismatch` and a reply that is not the protocol `ErrProtocol`.
The endpoint decodes each request strictly, as the attempt endpoint does, and answers a malformed, unversioned, newer, extended or unknown request, or one for another session, with an error and no turn end.
`Endpoint.TurnEnded` receives one value per answered turn-ended request, delivered before the reply, holding one: requests that arrive while it is unread coalesce.
The endpoint does not authenticate its peers; access control is the session directory's.

## Tests

`session_test.go` was written red first against stub bodies ([red run](../evidence/director-handoff-2026-10-22-red.txt)): the path rule and its refusals; a child process, the test binary playing the end-of-turn hook, signalling the supervisor across a real process boundary, a signal for another session refused and not received, and two unread signals coalescing; no supervisor before binding and after `Close`, which removes the socket; binding over a stale socket, refusing a live endpoint without disturbing it and a regular file, which is kept; and malformed requests answered with an error and never received.
The regular-file case was red against the first implementation, because a dial to a regular file on Linux is refused like a stale socket; the stale-file rule now checks the file is a socket first.
