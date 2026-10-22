# The Director's session record

The minimal Director needs a durable answer to "which Director session is current and what state is it in".
Slice 4 plans the session supervisor (`director start`) to write it, the pause, resume and stop commands to move it, and the status feed to report it; the wake that tells a running Director a message arrived records its decisions beside it.

Sessions and wakes live in a new top-level `director` bucket, created on first use as `workflow-approvals` was, so the store schema stays 1 and an existing file accepts them unchanged.
They are not per repository, because one Director spans every repository in its store.

A session is numbered from 1, starts `recovering` and moves only forward, through `running` or `paused`, `stopping` and `stopped`, except that `running` and `paused` move to each other.
`paused` is a state of its own, not a flag, because the Guildmaster's pause stops new work only while running attempts finish, so a paused session is still alive and must still be stoppable.
`stopping` is the state between the Guildmaster's stop and the runtime ending, which the planned stop command uses to end the Director's Artisan attempts and let the Director write its handoff.

Starting session n+1 records any session that has not stopped as `stopped`, in the same transaction, so exactly one session is ever current and a reader listing sessions never sees two running; the store records no reason, because the newer session's start time already says why.

A state move names the receipt sequence of the state it leaves: its mutation id is `director-state:<n>:<state>:<from>`, so the same move retried after a lost result replays, and a move decided on a stale read, such as a pause racing a stop, is refused rather than applied over the newer state.
The record carries that sequence as `StateSequence`, a field beyond the planned shape that the compare-and-set needs.

A wake is one record per message to the Director, keyed by its receipt sequence with the mutation id `wake:<message id>`, so the wake can find a replayed send's decision and type nothing new.
It records the sender type, the input a per-sender wake budget reads, so adding the budget changes no record shape.
The store checks only the record's form and that the message exists and is addressed to the Director; deciding to wake is the wake's job.

`director status` now reports the newest session from the same read transaction as the rest of the feed.
It does not yet probe the supervisor's identity for liveness or list a `recovering` session in `needs_me`; both wait for the supervisor that writes the record.
