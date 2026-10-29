# `internal/herdr`

## Contract

`Client` names an explicit non-default session on every call; tab ownership restrictions and the pane self-report exception are defined in its [API contract](../../internal/herdr/herdr.go).
Its process boundary, `Client.run`, passes argv and env exactly, keeps stdout and stderr apart, reports a nonzero exit with its output, errors when a program cannot start, and reports cancellation as the context's error.
Every run ends when its context ends: the program is killed and its output pipes, which a child of the program may still hold, are closed five seconds later at most (`WaitDelay`), so a call is bounded by its caller's context.
`ReportAgent` reports a pane's agent with `pane report-agent`, passing the resume command after `--` and refusing, before any call, a malformed pane, source, agent or state and a resume command that starts with a path or an option (`ErrResumeArgv`).
Why Herdr lost a running Artisan pane's reported agent, and the fix options, are in the [agent report loss findings](../evidence/herdr-agent-report-loss-2026-10.md).
`RecordedTab` makes an `OwnedTab` from a durable Guildhall record once `tab get` shows the tab still in the recorded workspace, refusing malformed ids before any call; a closed tab is not found.
On an owned pane, as `RunInOwnedTab` is, `PaneTitle` sets the pane's display title under a source with `pane report-metadata <pane> --source <id> --title <title>`, or clears it with `--clear-title` for "", refusing a malformed source or a multi-line title before any call.
It exists in Herdr 0.9.5, the floor ([Phase 0 probes](../evidence/director-input-2026-10-27.txt)); the session supervisor's wake notice is the pane title ([decision](../decisions/director-wake-hook.md)).
`Notify` shows a transient toast on the client's session with `notification show <title> --body=<body>`, the body joined to its flag so a body starting with `-` is never read as an option and left out when empty, refusing an empty or multi-line title and a multi-line body before any call; it is not tied to a pane, and it is how `messages send` [surfaces a note](../messaging.md#surfacing-a-note), bounded by its caller's context. It exists in Herdr 0.9.5 (the same probes).
`CheckOwnedPane` reads `pane get` and refuses a recorded pane moved out of its tab or workspace before presentation cleanup.
`RecordedTab`'s tab in another Herdr workspace and `CheckOwnedPane`'s moved pane are errors matching `ErrMoved`.
`CloseOwnedWorkspace` runs `workspace close <id> --session <s>` only with an `OwnedTab` of this client's session in that Herdr workspace, refusing malformed ids before any call; it checks nothing else about the Herdr workspace, so its caller (`adapter.IdleWorkspaceCloser`) revalidates the whole Herdr workspace first.
The client closes owned tabs and owned Herdr workspaces, never Herdr sessions or servers, and refuses the default session at `New`.
`WorkspaceCreate` validates the workspace, starting tab and root pane from the same creation response and returns the initial tab's ownership proof; `WorkspaceList` never grants tab ownership.
`PlaceWorkspace` is an optional presentation operation: it moves only a newly created workspace with response-derived ownership, after its exact Director and already adjacent, journal-bound siblings in launch order.
It requires named-session status, protocol 16 or newer and an absolute socket path from that status, never ambient socket identity.
It refuses missing or duplicate identities, renamed or detached siblings, interleaving and unowned child labels.
An already-adjacent child needs no move; otherwise it verifies the entire returned order after exactly one `workspace.move` request.
Ordering errors grant no retry, rollback, rename, close or task-record authority; callers warn and continue launching.
`ShellCommand` renders argv so the named shell starts exactly that argv, or refuses (D20).

## Tests

`order_test.go` covers exact launch-order placement, already-adjacent no-op, closed siblings, ownership refusal, unavailable or old protocol/socket/method, missing and duplicate identities, renamed/detached/interleaved siblings, foreign child blocks, malformed/mismatched responses and full-order verification.
The ordering implementation began as a refusing stub; its success and transport-reaching cases failed in the [red run](../evidence/artisan-child-workspaces-2026-10-26-order-red.txt), while refusal cases already passed against that stub.
`order_unix_test.go` executes the real Unix socket transport against a local server and checks the whitelisted request, response, cancellation and missing socket.
The launcher child-label assertion failed against the [old label](../evidence/artisan-child-workspaces-2026-10-26-label-red.txt) before implementation.
`TestArtisanChildWorkspaceHerdrLab` executes the real CLI adapter, recorded Director binding, presentation journal and socket transport in an isolated named session; the [live run](../evidence/artisan-child-workspaces-2026-10-26-live.txt) shows two children in launch order and the guarded teardown with its default-session tripwire intact.
`runner_test.go` (shared contract test, real `execRun` and `fakeRunner`; the case of a program whose child outlives it and holds its pipes was written red first ([red run](../evidence/watcher-advance-and-wake-2026-10-22-red.txt)): the call returned only when the child exited, and now returns at its context), `herdr_test.go` (client against recorded Herdr 0.9.5 responses; the `ReportAgent` cases were written red first, and the malformed pane, source, agent and state refusals passed against the stub, which refused everything), `shell_test.go`, `shell_exec_test.go` (round trips executed in each installed shell, every argument's line typed in turn into one run of that shell and checked by its own echo file; a missing shell the runner requires fails; the Director's wake line is a round-trip argument, which passed on its first run).
`TestRecordedTabChecksTheTabStillExists` was written red first against a stub ([red run](../evidence/director-wake-2026-10-22-red.txt))
`TestCloseOwnedWorkspace` checks the exact argv `herdr workspace close w1 --session guildhall-lab`, the stripped ambient `HERDR_*` variables, refusals of a zero or foreign-session `OwnedTab`, another Herdr workspace and an option-shaped id before any call, and the default-session refusal; it was added with the implementation and was not run red.
`TestPaneTitleUsesOwnedPane` checks the set and clear calls' exact argv against the probed command shape and that a zero tab or a multi-line title reaches no Herdr call; it was added with the call and not run red.
`TestNotifyShowsOneLineToast` checks `Notify`'s exact argv, with and without a body, and that an empty or multi-line title or a multi-line body reaches no Herdr call; it was added with the call and not run red.
