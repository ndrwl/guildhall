# The Director's wake

A stored message is not proof that anyone has seen it, and the minimal Director needs to hear about a message while its session runs, without a service (R1.29).
Slice 4 plans every sender to the Director to share one wake, over the [session record](director-session-record.md).

## The wake

`messaging.Wake` is the one wake function, called after every send to the Director: the engine's outbox (`task start`, `step done`, `step retry`) and `messages send ... director`.
The launcher's own stall send is planned and not built; when it lands it calls the same function as `guildhall`.
It reads the newest session; only a `running` or `recovering` session with a bound pane is woken, by typing one fixed line, `guildhall: <n> unread (<type> <count>, ...)`, carrying counts and never message text, so no sender can type instructions into the Director's session.
Every other case types nothing and records `queued` with the reason, such as `no session`, `no pane`, `paused` or `stopped`; the message still waits in the inbox for the Director's next turn.
A paused session is not woken: pausing is how the Guildmaster quiets the Director, and the next resume shows what needs it.

Each decision is a `Wake` record with the mutation id `wake:<message id>`, so a replayed send finds the wake already recorded and types nothing new, and the sender type each wake records is the input a per-sender wake budget will read without a record change.
The sender types are `guildhall` for the engine and `local` for `messages send`: the command cannot tell the Guildmaster from an Artisan or firstmate, so it records what it knows, which the planned `guildmaster` sender type of [Director messaging](0049-director-messaging.md) will refine once a sender is identified.
Both wake on every message; no budget exists yet.

The line is typed before the decision is recorded, so a crash in between types it again on the retry; two concurrent wakes for one message may both type, and the first recorded decision stands.
A typed line that is not seen costs nothing, while a recorded `woke` with nothing typed would be a lie.
A failed wake never fails the send: the message is durable first, and the command reports the failed wake on standard error.

## Typing into a pane Guildhall created in another process

The Director's pane is created by `director start`, and the wake runs later in another process, so the in-process ownership proof `herdr.OwnedTab` cannot be carried across.
`herdr.Client.RecordedTab` makes an `OwnedTab` from a durable Guildhall record after checking that the tab still exists in the recorded session and workspace: R1.54 holds because only Guildhall writes the record, and a closed tab is not found rather than typed into.
The adapter's terminal boundary gains `AdoptTab`, run by its three hosts (`HerdrTabs` over a stub Herdr process with the recorded response shapes, the in-package double and `adaptertest.ShellPanes`) under the shared contract test, with a second host on the same terminal standing in for the later process.
Typing goes through that boundary and refuses a pane whose shell is idle, so a wake line for a runtime that has exited never reaches a shell prompt; the wake then records `runtime not running`.

The wake takes the store through a narrow interface rather than an open store, because typing into Herdr may take seconds and the store's one-writer lock must not be held meanwhile; the commands open the store for each record operation, as the engine's records already do.
