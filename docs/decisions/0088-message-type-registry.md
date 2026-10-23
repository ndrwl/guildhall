# D88. One registry of the message types Guildhall sends

Sustainability review 11 (F40) found the types of the Director messages Guildhall sends enumerated twice: as literals at each send in `internal/interp` and as a hand-kept list in `internal/store` that the wake line and `DirectorUnread` counted by.
`store` cannot import `interp`, so the list could be kept only by reading the engine; one review round had already added `workspace-kept` to it after the fact, and `director-stage` was missing, so the wake counted it as `other`.

## The registry lives in the store

`store.MessageTypes` maps each type Guildhall sends to its sender, `engine`, `launcher` or both, and holds the documented local kinds `question` and `note`; each type is a `store` constant.
`DirectorUnread`, and so the [wake](../messaging.md#waking-the-director) line, counts by it and counts every other type as `other`, so no sender-chosen type is ever typed into the Director's pane.
The store is the one package every sender already imports, so the registry needs no new dependency.

## The engine sends by constant, refused at every send

`internal/interp` names a message's type only as a registry constant; its own `AdvanceFailed`, `WorkerStalled`, `DirectorStage` and `BudgetRefused` constants are gone.
`directorMessage`, the one function every engine and launcher send of a Director message goes through, refuses a type not in the registry with an error naming it and sends nothing, so a new message type is added to the registry by the change that sends it.
The check is at the send rather than in a test reading the package's sources, so a send that only some workflows reach still fails the first time it runs; the engine's test envs also fail on any unregistered type their suites send.
The store's `SendMessage` and `messages send` do not check: a local sender may still choose any type, which counts as `other`.

## Not changed

`director status`'s `messages.by_type` still counts every type by name: it is JSON the Director reads, not a line typed into its pane.
Message ids keep their existing prefixes, such as `stage.` for `director-stage`, so a replayed send after an upgrade still finds its first send.
