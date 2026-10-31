---
status: accepted
date: 2026-10-30
topic: director
tasks: d79
amends: director-pause-stop.md, director-skills.md
amended-by: copy-discovery.md
formerly: D82, 0082-director-handoff-reload.md, D94, 0094-director-notes.md, plain-reload-ignores-director-stages.md
---

# A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage

## Context

A Director session's context runs out, its settings change and it is stopped, so a session must hand over to the next one and end at a point that loses nothing.
The first handoff design kept one large handoff note, which sessions did not keep current, and its reload waited for open grills, which can stay open for days.

## Decision

- **Notes are small store records.**
  `director note add|edit|remove|list` keep one-line notes (at most 1024 bytes) in the store, in three kinds: `promised`, `guildmaster` and `plan`, recording only what no other record holds; every add, edit and remove is its own version record ([notes](../director.md#notes), [store](../store.md#director-notes)).
  Only the newest session that is not `stopped` writes them; every new session's `startup.md` lists every live note; commands print one-line receipts, the status feed carries no notes, and startup asks the Director to compact them past 8 KB without refusing a write.
- **Reload requests collapse.**
  At most one `director reload` request is `requested`; repeating it returns it and `--now` upgrades it in place; the applying session records `applied` and the old one `stopped` as `reloaded`; with no supervisor running the next `director start` applies it ([reload](../director.md#reload)).
- **One end-of-turn rule for stop and reload.**
  The supervisor decides at each turn signal and each poll with one pure function, `decideEnd`: a stop or `--now` ends the runtime at the next turn end; a plain reload at a quiet point, a turn ended since the last turn start and the newest wake ([the end of a turn](../director.md#the-end-of-a-turn)).
  A running Director stage, such as a grill, holds neither: the next session restarts it, and a reload never touches attempts.
  `--now` is refused only with no session or a `stopped` one.
- **Turn signals.**
  Claude Code's `UserPromptSubmit` hook runs `director turn-started` and its `Stop` and `StopFailure` hooks `director turn-ended`, over the session directory's `supervisor.sock` with a versioned line protocol naming the session number ([session contract](../contracts/session.md)).
  The Director is idle while the newest signal is a turn end and no wake reached it since, or after `[director] idle_after` (5 minutes) with no signal, which covers a turn interrupted with Esc.
  Without a hook or an endpoint the 60-second grace stands in for the turn end; with a hook the grace also ends a stop or `--now` once the Director is idle.
  The signals exit 1, never 2, on any failure, because exit 2 keeps Claude going or blocks the prompt.
  `runtimes.Session.TurnEndHook` declares the capability; the fake's scripts send the signals themselves (R1.24).
- **A reload resolves the runtime again** from the copy's current `[director] adapter`, unless `director start` named one; a refused runtime ends the supervisor with exit 1 and leaves the reload requested.

## Alternatives rejected

- Waiting for a turn end after each request, without a turn-start signal: an idle Director would never reload.
- A single handoff-note document: it went stale (`handoff-645` was served to sessions 11 to 16) and was rewritten whole.
- Kinds for an open grill or for suggested skills: a grill is restarted, and the skills are the Director's own.
- Migrating old handoff notes or showing the last one once more: the Guildmaster ruled "lets remove that old flow".
- A `stop-requested` message asking for a note before a stop: notes kept as the Director goes cover a stop.
- Moving the socket elsewhere when the session directory is too long for AF_UNIX: the hook finds it from the copy and session number alone, so the supervisor falls back to the grace instead.

## Consequences

A turn running longer than `idle_after` with no signal is taken as idle; `director stop --now` stays the immediate escape.
A new session learns of an open grill only from its unread `director-stage` message or the task steps in `director status`; a startup line per open Director stage is a follow-up.
Stores keep their `handoffs` records and `store doctor` still decodes them, but nothing writes or shows them.
[Copy discovery](copy-discovery.md) changed the hooks to name the copy with `--copy`.
This replaced the pause-and-stop record's interim rule that a plain stop's grace stands in for the end of a turn, and the handoff skill's `director handoff --note-file` step in [the Director's skills](director-skills.md).

## History

- 2026-10-22, D82 (0082-director-handoff-reload.md): the first half of the slice 4 handoff plan.
  A handoff note was a document under `<state>/documents/director/handoff-<seq>.md`, recorded by `director handoff --note-file` inside the recording mutation with its sha256, read with the decision-answer rules and at most 64 KiB, as R1.13 asks of design documents.
  `--now` needed a fresh note from the current session, written after any running Director stage started (the "never mid-grill" rule), and a plain reload also waited for no Director stage running.
  It set request collapsing, `decideEnd`, the turn-started signal, `StopFailure` and `idle_after`, the session endpoint, exit 1 for the signals, `TurnEndHook` and re-resolving the runtime.
  The turn-started signal exists because the captain starts most foreground turns by typing, which no wake records; the endpoint keeps the newest unread signal, and a turn end that arrived before a request is not taken as its turn end.
  The hooks named no path and inherited `GUILDHALL_COPY`, so no path had to be quoted for any shell; the slice plan's `--copy <dir>` was dropped then, and copy discovery later restored it.
- 2026-10-29, D94 (0094-director-notes.md): sessions left the one note stale and re-checked items already resolved, and the Guildmaster asked for small pieces that can be modified and removed, with low token use.
  Notes replaced the handoff note, `director handoff` was removed, and `--now` lost its note and running-stage checks because the Guildmaster ruled a grill can be restarted ("its fine we can restart the grill").
  Each version is a record, since receipts keep only digests; mutation ids are `note-add:<session>:<sha256(kind|task|text)>`, `note-edit:<id>:<sha256(text)>:<prior version>` and `note-remove:<id>`.
- 2026-10-30, plain-reload-ignores-director-stages.md (d79): a plain reload asked for at 21:26:14 on 2026-10-30 stayed `requested` while the grills for d75, d76 and d77 were open, because D94 had not discussed the plain reload.
  `decideEnd` stopped reading running Director stages, so a store error there can no longer hold a reload, and `--now` and a plain reload now differ only in timing.
