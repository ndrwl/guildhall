# `internal/session`

## Contract

The Director session supervisor's control endpoint ([the end of a turn](../director.md#the-end-of-a-turn), [decision](../decisions/0082-director-handoff-reload.md)).
`EndpointPath` is `supervisor.sock` in an absolute session directory, `ErrPath` for a relative directory or a path longer than `artisan.MaxEndpointPath`.
`Listen` binds an AF_UNIX stream socket there for one session number, on Windows as on Unix, removing a leftover socket nothing listens on and refusing, and keeping, one that answers or any file that is not a socket; `Close` stops it and removes the socket file, idempotently.
`Signal` sends one `guildhall.session-control/1` request line naming the session and the turn signal, `TurnStarted` (op `turn-started`) or `TurnEnded` (op `turn-ended`), and waits for the reply line; nothing listening is `ErrNoSupervisor`, an endpoint for another session `ErrMismatch` and a reply that is not the protocol `ErrProtocol`.
The endpoint decodes each request strictly, as the attempt endpoint does, and answers a malformed, unversioned, newer, extended or unknown request, or one for another session, with an error and no turn signal.
`Endpoint.Turns` receives the answered turn signals in order, each delivered before its reply, holding one: a signal that arrives while another is unread replaces it, so the unread value is always the newest.
The endpoint does not authenticate its peers; access control is the session directory's.

The session's context report ([automatic handoff](../director.md#automatic-handoff)) is `context.json` (`ContextFile`) in the session directory: `WriteContext` replaces it atomically with a `guildhall.session-context/1` document holding `used_percent` and `reported_at`, refusing a relative directory or a percent outside 0 to 100 and writing nothing; `ReadContext` decodes it strictly, a missing file wrapping `fs.ErrNotExist`.
`NumberEnv`, `GUILDHALL_SESSION`, is the session number the supervisor sets in the runtime's environment, so the reporter finds its directory without the store.

## Tests

`session_test.go` was written red first against stub bodies ([red run](../evidence/director-handoff-2026-10-22-red.txt)): the path rule and its refusals; a child process, the test binary playing the end-of-turn hook, signalling the supervisor across a real process boundary, a signal for another session refused and not received, and two unread signals coalescing; `TestTurnStartSignalsTheSupervisor`, added with the turn-start signal and not run red, has a child process signal a turn start, refused for another session, and sees the newer of two unread signals replace the older in either order; no supervisor before binding and after `Close`, which removes the socket; binding over a stale socket, refusing a live endpoint without disturbing it and a regular file, which is kept; and malformed requests answered with an error and never received.
The regular-file case was red against the first implementation, because a dial to a regular file on Linux is refused like a stale socket; the stale-file rule now checks the file is a socket first.
`context_test.go` was written red first against stub bodies ([red run](../evidence/director-handoff-triggers-2026-10-22-red.txt)): reports written and read back whole, each replacing the last, naming the schema; `TestContextReportRefusals`, a percent out of range or a relative directory writing nothing and a malformed, unversioned, newer, extended or out-of-range report refused, passed against the stubs, which refuse everything.
