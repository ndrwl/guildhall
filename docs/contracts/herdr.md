# `internal/herdr`

## Contract

`Client` names an explicit non-default session on every call; tab ownership restrictions and the pane self-report exception are defined in its [API contract](../../internal/herdr/herdr.go).
Its process boundary, `Client.run`, passes argv and env exactly, keeps stdout and stderr apart, reports a nonzero exit with its output, errors when a program cannot start, and reports cancellation as the context's error.
Every run ends when its context ends: the program is killed and its output pipes, which a child of the program may still hold, are closed five seconds later at most (`WaitDelay`), so a call is bounded by its caller's context.
`ReportAgent` reports a pane's agent with `pane report-agent`, passing the resume command after `--` and refusing, before any call, a malformed pane, source, agent or state and a resume command that starts with a path or an option (`ErrResumeArgv`).
`RecordedTab` makes an `OwnedTab` from a durable Guildhall record once `tab get` shows the tab still in the recorded workspace, refusing malformed ids before any call; a closed tab is not found.
`CheckOwnedPane` reads `pane get` and refuses a recorded pane moved out of its tab or workspace before presentation cleanup.
`WorkspaceCreate` validates the workspace, starting tab and root pane from the same creation response and returns the initial tab's ownership proof; `WorkspaceList` never grants tab ownership.
`PlaceWorkspace` is an optional presentation operation: it moves only a newly created workspace with response-derived ownership, after its exact Director and already adjacent, journal-bound siblings in launch order.
It requires named-session status, protocol 16 or newer and an absolute socket path from that status, never ambient socket identity.
It refuses missing or duplicate identities, renamed or detached siblings, interleaving and unowned child labels.
An already-adjacent child needs no move; otherwise it verifies the entire returned order after exactly one `workspace.move` request.
Errors grant no retry, rollback, rename, close or task-record authority; callers warn and continue launching.
`ShellCommand` renders argv so the named shell starts exactly that argv, or refuses (D20).

## Tests

`order_test.go` covers exact launch-order placement, already-adjacent no-op, closed siblings, ownership refusal, unavailable or old protocol/socket/method, missing and duplicate identities, renamed/detached/interleaved siblings, foreign child blocks, malformed/mismatched responses and full-order verification.
The ordering implementation began as a refusing stub; its success and transport-reaching cases failed in the [red run](../evidence/artisan-child-workspaces-2026-10-26-order-red.txt), while refusal cases already passed against that stub.
`order_unix_test.go` executes the real Unix socket transport against a local server and checks the whitelisted request, response, cancellation and missing socket.
The launcher child-label assertion failed against the [old label](../evidence/artisan-child-workspaces-2026-10-26-label-red.txt) before implementation.
`TestArtisanChildWorkspaceHerdrLab` executes the real CLI adapter, recorded Director binding, presentation journal and socket transport in an isolated named session; the [live run](../evidence/artisan-child-workspaces-2026-10-26-live.txt) shows two children in launch order and the guarded teardown with its default-session tripwire intact.
`runner_test.go` (shared contract test, real `execRun` and `fakeRunner`; the case of a program whose child outlives it and holds its pipes was written red first ([red run](../evidence/watcher-advance-and-wake-2026-10-22-red.txt)): the call returned only when the child exited, and now returns at its context), `herdr_test.go` (client against recorded Herdr 0.9.5 responses; the `ReportAgent` cases were written red first, and the malformed pane, source, agent and state refusals passed against the stub, which refused everything), `shell_test.go`, `shell_exec_test.go` (round trips executed in each installed shell, every argument's line typed in turn into one run of that shell and checked by its own echo file; a missing shell the runner requires fails; the Director's wake line is a round-trip argument, which passed on its first run).
`TestRecordedTabChecksTheTabStillExists` was written red first against a stub ([red run](../evidence/director-wake-2026-10-22-red.txt))
