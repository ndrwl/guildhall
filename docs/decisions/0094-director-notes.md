# D94. Director notes replace the handoff note

D82 gave a Director one way to carry what the store does not hold into its next session: a single handoff-note document, written at the end of a session with `director handoff --note-file` and named in every later `startup.md`.
In practice the note was a large blob rewritten at each handoff, or not rewritten at all: one note, `handoff-645` from session 10, was served to sessions 11 to 16 because no later session recorded one, and each of them re-checked items it had already resolved.
The Guildmaster asked for small pieces of information that can be modified and removed instead, written by the Director as things come up, and for the plan to keep token use low.
This supersedes D82's first decision, "The note is a document, the store records it", and its third, "`--now` needs a fresh note", with the never-mid-grill rule that came with it.

## Decisions

- **Notes are small store records.**
  `guildhall director note add --kind <kind> [--task <repo>/<id>] <text>`, `note edit <id> <text>`, `note remove <id> [--reason <text>]` and `note list` keep the Director's notes in the store's `director/notes` bucket, with every add, edit and remove in `director/note-versions` ([store](../store.md#director-notes)).
  A note records only what no other record holds, in one of three kinds: `promised`, a promise to the Guildmaster; `guildmaster`, a preference or context they gave; `plan`, Director intent.
  There is no kind for an open grill or for suggested skills: a grill is restarted, and the skills are the Director's own.
- **Only the newest live session writes them.**
  The store refuses a write from a session that is not the newest or is `stopped`, in the write's own transaction, as it did for a handoff note; Artisans cannot run `guildhall` at all.
- **Every new session gets every live note.**
  `startup.md` lists them under `## Notes`, before the in-flight snapshot, grouped by kind, one line each with the id, task, session and age; a note whose task is `done` or `cancelled` is flagged `[task done, check it]` and never removed automatically.
- **Minimal tokens.**
  A note is one line of at most 1024 bytes, each command prints only a one-line receipt such as `n7 added`, and the status feed carries neither notes nor the old `handoff_note`: `note list` covers them.
  The per-note cap does not bound the total, so when the live notes' text passes 8 KB, startup opens the section with a line asking the Director to compact them; it is a warning, and no add or edit is refused for the total.
- **History is records, not receipts.**
  Receipts keep only request digests, so each version is its own record; a removed note stays, as an unparked attempt's park record does.
  Mutation ids make each change replay: `note-add:<session>:<sha256(kind|task|text)>`, `note-edit:<id>:<sha256(text)>:<prior version>`, so the same text set again after another edit is a new version, and `note-remove:<id>`.
- **`--now` needs no note and does not wait for a grill.**
  `director reload --now` is refused only with no session or a `stopped` one.
  The Guildmaster ruled that a grill in progress can be restarted ("its fine we can restart the grill"), so the running-stage check goes with the note check.
- **The old note is retired, not migrated.**
  `director handoff` is removed and startup no longer reads handoff notes, with no legacy section and no rule to show the last one once more, as the Guildmaster ruled ("lets remove that old flow").
  Stores keep their `handoffs` records, and `store doctor` still decodes them.

## Not changed

`director reload`, request collapsing and the turn-end reload; the automatic handoff triggers and their `; handoff due (<reason>)` wake suffix, which now ask the Director to check its notes and reload; and `stop-requested`, which stays out of scope since notes kept as the Director goes cover a stop.
