# A plain reload does not wait for running Director stages

A plain `guildhall director reload` no longer waits for every running Director stage attempt to finish.
It applies at its quiet point alone: a turn has ended since the last turn start and the newest wake, or the Director counts as idle, or, with no hook, once the 60-second grace has passed.

## Why

[D82](0082-director-handoff-reload.md) made a plain reload wait for a quiet point with no Director stage running.
[D94](0094-director-notes.md) dropped that check for `--now`, because the Guildmaster ruled that a grill in progress can be restarted ("its fine we can restart the grill"), but left the plain reload unchanged without discussing it.
A grill waiting on the Guildmaster's answers stays running for hours or days, so a plain reload asked for while one is open never applied.
On 2026-10-30 a plain reload requested at 21:26:14 stayed `requested` while the grills for d75, d76 and d77 were open, and applied only after `director reload --now`.
D94's reason holds for a plain reload too: the next session restarts the grill, and a reload never touches attempts, so the handed grill attempt survives unchanged.

## What changes

`decideEnd` no longer reads whether a Director stage is running, and the supervisor no longer reads the running Director stages before applying a plain reload, so a store error there can no longer hold one.
`--now` and a plain reload now differ only in timing: `--now` applies at the end of the current turn, and a plain reload at a quiet point.
Both are admitted, and applied, while a Director stage attempt runs.
D82's "One end-of-turn rule" bullet is superseded for its "no Director stage running" clause.

## Not changed

`--now`, stop handling, the grace and `idle_after` rules, request collapsing, the runtime-ends-by-itself path, re-exec and the handoff triggers.

## Known follow-up

The startup snapshot leaves out a handed Director stage with no result, so a new session learns of an open grill only from its unread `director-stage` inbox message or from the task steps in `director status`.
`--now` has had this exposure since D94, and this change makes it more frequent rather than creating it; a startup line for each open Director stage is a follow-up.
