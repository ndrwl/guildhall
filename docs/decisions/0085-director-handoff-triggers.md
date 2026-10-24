# D85. Automatic handoff triggers and the context reporter

A Director session's context fills up as it works, so the session should hand over before it runs out rather than when the captain notices.
This is the second half of the slice 4 handoff plan (D11b), after [handoff notes and reload](0082-director-handoff-reload.md): three triggers that tell the Director a handoff is due.
The original status-line input was superseded by [core transcript context](lanterns-and-core-context.md); [automatic handoff](../director.md#automatic-handoff) owns current reporter behavior and legacy compatibility.

## Decisions

- **Three triggers, in `[director]`.**
  `handoff_after_tasks` (0, off, unless set) counts tasks moved to `done` or `cancelled` since the session was recorded; `handoff_context_percent` (70 unless set, 0 off) compares the context the runtime last reported as used; `handoff_idle` (`0s`, off, unless set) is how long the Director may go with no turn signal and no wake.
  A trigger tells the Director a handoff is due; it never reloads the session itself, because only the Director can write the note and only a note keeps an open grill (the "never mid-grill" rule of [reload `--now`](0082-director-handoff-reload.md)).
- **The trigger rides on a wake line, so no extra model turn is spent.**
  When a wake types its line and a trigger holds, the wake appends `; handoff due (<reason>)`, such as `; handoff due (context 72%)` or `; handoff due (3 tasks finished)`, two reasons joined with `, ` when both hold, and records a `HandoffDue` per trigger once the line is typed.
  The idle trigger has no message to ride on: the supervisor's one timer types the same line, the unread counts with the suffix, once the Director has been quiet for `handoff_idle`.
- **Once per crossing.**
  The `HandoffDue` mutation id names the session, the trigger and the crossing, so a crossing is announced once: the task trigger's `k`th crossing is the `k × handoff_after_tasks`th finished task, so a Director that carries on past a reminder is reminded again after as many tasks more; the context trigger crosses once per session, since context rarely shrinks within one and a new session starts afresh; the idle trigger's `k`th crossing is the `k`th quiet period, which a turn signal or a wake ends.
  A wake that fails to type records no crossing, so the next wake carries it; two wakes racing may both carry it, as two wakes for one message may both type.
- **The session record carries the policy.**
  A wake runs in whichever process stored the message, which may have no config, so the supervisor records the session's thresholds and the absolute path of its `context.json`, when the runtime reports context, in the `DirectorSession` it starts.
  A reload re-reads the config, so a changed threshold takes effect with the next session.
- **The reporter writes a file, not the store.**
  See [automatic handoff](../director.md#automatic-handoff) for the core reporter and legacy status-line command.
- **The context report is an optional capability (R1.24).**
  See the [`runtimes.Session` contract](../contracts/adapter-runtimes.md) for `ContextReport`.
- **The status feed shows the newest report.**
  The session section carries `context` with the used percent and when it was reported, read from the recorded path, and is absent when nothing was reported.

## Not yet

The `stop-requested` message that asks the Director to write a note before a stop is a separate slice.
Cost and token totals from the status-line input are not recorded; spend stays with the attempt meter.
