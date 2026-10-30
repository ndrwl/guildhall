# D82. Handoff notes, `director reload` and the end of a turn

A Director session's context runs out, its settings change and it is stopped, so a session must be able to hand over to the next one and end at a point that loses nothing.
This is the first half of the slice 4 handoff plan (D11a): the note, `director reload [--now]`, the turn signals and the supervisor's loop.
The automatic triggers (`handoff_after_tasks`, `handoff_context_percent`, `handoff_idle`) and the Claude status-line reporter `director context-report` are the second half.

## Decisions

- **The note is a document, the store records it.**
  `director handoff --note-file <path|->` writes the note to `<state>/documents/director/handoff-<seq>.md` and records a `HandoffNote` with the session, path and sha256, as R1.13 asks of grilling answers and design documents.
  The store chooses the path from the receipt sequence and has the caller write the file inside the recording mutation, before it commits, so a recorded note always has its file and a failed write records nothing.
  The note is read with the decision-answer rules (BOM dropped, CRLF to LF, UTF-16 refused) and is at most 64 KiB.
  Its mutation id carries the session, the digest and the newest note before it, so the same note handed off twice in a row is one record, and a note handed off again after another becomes the newest anew.
  Superseded by [D94](0094-director-notes.md): Director notes replace the handoff note, and `director handoff` is gone.
- **Reload requests collapse.**
  At most one request is `requested`; repeating it returns it unchanged and `--now` upgrades it in place, so a Director that types `reload` twice, or the Steward and the Guildmaster both asking, restart the session once.
  The session that applies it records `applied` with the old and new session numbers; the old session is recorded `stopped` with the end reason `reloaded`.
  A request with no supervisor running is applied by the next `director start`, as release upgrades specify.
- **`--now` needs a fresh note.**
  It is refused unless the newest note was recorded by the current session and, while a Director stage attempt runs, after that attempt started, compared by receipt sequence.
  This is the "never mid-grill" rule: a reload during a grill carries the open questions in a note written for it.
  Superseded by [D94](0094-director-notes.md): `--now` needs no note and does not wait for a running Director stage, since the next session restarts a grill.
- **One end-of-turn rule for stop and reload.**
  The supervisor decides at each turn signal and at each poll with one pure function, `decideEnd`, tested as a table.
  A stop or a reload `--now` ends the runtime at the next turn end; a plain reload waits for a quiet point, a turn ended since the last turn start and the newest wake and no Director stage running.
  Superseded by [`decisions/plain-reload-ignores-director-stages.md`](plain-reload-ignores-director-stages.md) for the "no Director stage running" clause: a running Director stage no longer holds a plain reload.
  Without a hook, or with no endpoint, the 60-second grace stands in for the turn end, as D10's stop already used it; with a hook the grace also ends a stop or `--now` once the Director is idle, so stopping an idle Director does not wait forever for a turn that never comes.
  A turn-ended signal that arrived before a request is not taken as that request's turn end.
- **Turns that start are signalled too.**
  In the foreground session the captain starts most turns by typing, and no wake records that, so a turn end alone would leave the Director idle for the rest of the session and let a plain reload, or a stop or `--now` past the grace, end it mid-turn.
  Claude's `UserPromptSubmit` hook runs `guildhall director turn-started`, one more op on the same endpoint, and the Director is idle only while the newest signal is a turn end and no wake has typed since.
  Waiting instead for a turn end after each request was rejected: an idle Director would then never reload.
  The endpoint keeps the newest unread signal, so a turn that starts before the supervisor reads the previous end is never taken as idle.
  A runtime without the hooks keeps the turn-end-only rule.
- **A quiet runtime is idle.**
  A turn can end with no turn end: Claude Code runs no `Stop` hook for a turn the captain interrupts with Esc, and runs `StopFailure` instead for one that ends in an API error.
  `StopFailure` is registered to send `turn-ended` as `Stop` does; for Esc, which no hook reports, a runtime with hooks counts as idle once no turn signal and no wake has arrived for `[director] idle_after`, 5 minutes by default, so a stop or `--now` ends it after the grace and a plain reload applies.
  The cost is that a turn running longer than `idle_after` with no signal is taken as idle; `director stop --now` stays the immediate escape.
- **The hook commands name no path.**
  The Claude hooks are `guildhall director turn-started` and `guildhall director turn-ended` with no `--copy`: the hooks inherit the session's `GUILDHALL_COPY`, so no path has to be quoted for the shell Claude Code runs hooks in, on any platform.
  The slice plan wrote `--copy <dir>`; the environment carries the same copy.
- **The endpoint is the session directory's.**
  `internal/session` binds `supervisor.sock` in the session directory with the attempt endpoint's stale-file rule and a versioned, strictly decoded line protocol naming the session number, so a hook from an older session cannot end a newer one.
  A session directory too long for an AF_UNIX path gets no endpoint and the supervisor falls back to the grace; it does not move the socket elsewhere, because the hook finds it from the copy and the session number alone.
- **The turn signals never exit 2.**
  A Claude Code `Stop` hook that exits 2 makes Claude keep going and a `UserPromptSubmit` hook that exits 2 blocks the prompt, so a usage error from `turn-ended` or `turn-started` is exit 1, unlike every other command.
- **A turn-end hook is a declared runtime capability.**
  `runtimes.Session` gains `TurnEndHook`: true for Claude Code, whose settings register both hooks, false for the fake, whose scripts run `turn-started` and `turn-ended` themselves where a test needs a turn to start or end (R1.24).
- **A reload resolves the runtime again.**
  The new session takes the copy's current `[director] adapter`, unless `director start` named a runtime, checked on `PATH` as the preflight does; a refused runtime ends the supervisor with exit 1 after the old session is recorded `stopped` as `reloaded`, leaving the reload requested for the next `director start`.

## Not yet

The `stop-requested` message that asks the Director to write a note before a stop, the three automatic triggers, `director context-report` and the context percent in the status feed land with the second half.
Moving a reloaded session onto a new release's binary lands with release pins.
