# One registry of the message types Guildhall sends

Sustainability review 11 (F40) found the types of the Director messages Guildhall sends enumerated twice: as literals at each send in `internal/interp` and as a hand-kept list in `internal/store` that the wake line and `DirectorUnread` counted by.
`store` cannot import `interp`, so the list could be kept only by reading the engine; one review round had already added `workspace-kept` to it after the fact, and `director-stage` was missing, so the wake counted it as `other`.

## The registry lives in the store

`store.MessageTypes` maps each type Guildhall sends to its sender, `engine`, `launcher` or both, and holds the documented local kinds `question` and `note`; each type is a `store` constant.
`DirectorUnread`, and so the [wake](../messaging.md#waking-the-director) line, counts by it and counts every other type as `other`, so no sender-chosen type is ever typed into the Director's pane.
The store is the one package every sender already imports, so the registry needs no new dependency.

## The engine sends by constant, checked at every send

`internal/interp` names a message's type only as a registry constant; its own `AdvanceFailed`, `WorkerStalled`, `DirectorStage` and `BudgetRefused` constants are gone.
`TestSentTypesAreRegistered` reads the package's sources and fails on any `notify` or `directorMessage` call whose type is not a `store` constant in the registry, so a new message type is added to the registry by the change that sends it.
A source check was chosen over a runtime one because a send that only some workflows reach would escape a test that runs workflows.

## Not changed

`director status`'s `messages.by_type` still counts every type by name: it is JSON the Director reads, not a line typed into its pane.
Message ids keep their existing prefixes, such as `stage.` for `director-stage`, so a replayed send after an upgrade still finds its first send.
