# Interpreter

`internal/interp` advances a task through the workflow revision it bound when it was created.
`interp.Engine.Advance(ctx, event)` is deterministic: it reads the task's store records, never a prompt or the Director's say-so, and makes no model call.
This slice runs the straight path only; loops, the review and checks gates, `publish`, `validate`, automatic merging and Director stages are refused with `ErrUnsupported`.
Nothing on the command line calls the engine yet: result intake, launch and the watcher are later work.

## Seams

The engine consumes four narrow interfaces, declared in `internal/interp` beside the code that calls them.

| Seam | Real implementation | Double | Shared suite |
|---|---|---|---|
| `Records`: read a task, its attempts, documents and decisions; start and finish attempts, record documents and open decisions | `*store.Store` | `interptest.Records` | `interptest.RunRecordsContract` |
| `Launcher` ([launch seam](mock-agent.md#the-launch-seam)) | none yet | `interptest.Script` | `interptest.RunLauncherContract` |
| `Prompter`: `render.Prompt` with the caller's baseline and skill roots | `PromptFunc` over `render.Prompt` | a recording `PromptFunc` | none: the real renderer runs in tests |
| `Outbox`: the send half of `messaging.Mailbox` | `messaging.StoreMailbox` | `messagingtest.Fake` | `messagingtest.RunMailboxContract` |

`Records` is the first narrow store interface: it lists only the methods the engine calls, as D9 and the [contract rules](contracts.md#which-boundaries-get-an-interface) allow.
The engine does not read workflow approvals; enforcing them at task creation is separate work.
`Engine.Workflow` returns the definition a task's revision names, and the engine refuses one whose name or digest differs (`ErrRevision`).
`Engine.Config` gives each profile's adapter and each repository's merge policy; a step without a profile runs `Engine.DefaultAdapter`.

## Events

| Event | Validated against the records | Recorded |
|---|---|---|
| `task-created` | The task exists and is open. | Nothing by itself. |
| `attempt-result` | The named attempt of the named step exists. | When it is still running: its result file, read with `adapter.ReadResult` for identity `<task>`, `<step>-<n>`; a succeeded result's outputs as documents; then the outcome. |
| `decision-answered` | The named decision exists and is answered. | Nothing by itself: the answer is already recorded. |

Any other event, or one that does not match the records, is refused with `ErrEvent` and changes nothing.

## Advance

After intake, the engine walks the steps in order, from the records, and stops at the first one that must be waited for.

- **Artisan step.** With no attempt yet, the engine checks the profile's adapter with `CheckAdapter` and resolves the inputs, then records attempt `n`, renders its prompt into `<state>/<repo>/<task>/<step>-<n>/prompt` and launches it with its result file at `<step>-<n>/result.json`; the task waits for the attempt. A succeeded attempt completes the step; a review step's outcome must be `approved`. A failed or stopped attempt waits for the Director, who is told through the outbox.
- **Guildmaster step.** The engine opens decision `<step>-<round>` with the question and options `render.Question` gives, bound to its subject, and waits. An `approved` answer completes the step; a Guildmaster approval then records the approved document as the accepted spec, `<step>-<round>-spec`.
- **Deliver.** Under the `approval` merge policy the engine opens the merge decision `deliver-<round>`, kind `merge`, with options `merge` and `hold`, bound to the head, and waits. Merging after the answer, and the `auto` policy, are later work.

A `changes_requested` outcome, from a review result or a Guildmaster answer, fails with `ErrUnsupported` and the words "loops in S6b"; any other outcome that is not `approved` fails with `ErrUnsupported`.

| Decision | Kind | Subject |
|---|---|---|
| `guildmaster-approval` | `approval` | The `spec` input document's digest |
| `document-review` | `review` | The `subject` input document's digest |
| `review` | `review` | The code head on the `head` input |
| `deliver` | `merge` | The code head on the `head` input |

Every decision the engine opens is also sent to the Director's inbox as message `<repo>.<task>.decision.<id>`, type `decision`, and every failed or stopped attempt as `<repo>.<task>.attempt-failed.<step>-<n>`, type `attempt-failed`.

## Documents and inputs

A succeeded result's outputs are copied from the task's working directory into the Director document directory at `tasks/<repo>/<task>/<step>-<n>/<output path>` and recorded with their sha256.
The first output is the step's output port, named `<step>-<n>-<port>`; later outputs add `-<index>`, from 2.
A code-head output must hold a 40 or 64 lowercase hex object id, and a review outcome must parse, or nothing is recorded.
An input `step.port` resolves to that port's document of the latest round, and its file is checked against the recorded digest before it is used.
The working directory is `<state>/<repo>/<task>/work` until worktree custody is defined.

## Spec gate

Every input of type accepted spec, such as `build`'s and `review`'s `spec`, must come from an approval decision answered `approved` whose subject digest equals the accepted spec's digest; otherwise the step is refused with `ErrSpecGate` before anything is recorded.
Recording the accepted spec checks the same: the approved document must be the one the decision was opened for.

## Replay and recovery

Every write uses a mutation id derived from the task and the record, such as `attempt:<repo>:<task>:<step>:<n>`, `attempt-finish:…`, `document:<repo>:<task>:<name>` and `decision:<repo>:<task>:<id>`, and messages use derived ids too.
The engine derives its position from the records, not from the event, so repeating any advance after a crash at any point replays its writes and records nothing twice.
An advance is several writes, one per record, each committed on its own; the ordering (outputs before the attempt's outcome, the attempt before its launch) makes every partial advance one that a repeat completes.
Once `Launch` returns, the engine marks the attempt directory with `launched.json`.
An attempt recorded but neither marked launched nor holding a result is rendered again and launched with the same identity, never recorded again.
A crash after `Launch` returns but before the mark relaunches the same identity; refusing a live duplicate belongs to the launcher's ownership record.
`Advance` is not safe for concurrent use on one task; the store's single writer serialises processes.

## Tests

`internal/interp/engine_test.go` runs the straight workflow, `testdata/workflows/straight.json` (plan, Guildmaster acceptance, build, Artisan review, Guildmaster sign-off, deliver), twice: against the scripted mock with the in-memory records and `messagingtest.Fake`, and against the same mock with a real store file and its mailbox.
It checks each event's effect on the records, the approval bound to the plan document's digest, the spec gate, a crash between recording and launching an attempt, the zero-model guard, refused loops, a failed attempt, mismatched events and a changed revision.
`TestRecordsContract` runs `interptest.RunRecordsContract` against the store and the double.
