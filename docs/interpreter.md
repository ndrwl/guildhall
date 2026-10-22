# Interpreter

`internal/interp` advances a task through the workflow revision it bound when it was created.
`interp.Engine.Advance(ctx, event)` is deterministic: it derives workflow progress from the task's store records, never from prompt instructions or the Director's say-so, and makes no model call.
It follows `changes_requested` loop rules with their `max`, asks the Guildmaster at a loop's exit, checks the review gate before `deliver`, and [publishes and merges](#publish-and-deliver) through the forge; the checks gate, `validate`, automatic merging, Director stages and loops back to a step not run by an Artisan are refused with `ErrUnsupported`.
[`task start` and `step done`](running.md) run it from the command line; the per-task watcher is later work.

## Seams

The engine consumes six narrow interfaces, declared in `internal/interp` beside the code that calls them.

| Seam | Real implementation | Double | Shared suite |
|---|---|---|---|
| `Records`: read a task, its attempts, documents, decisions and rulings; start and finish attempts, record documents, open decisions and contest rulings | `cli.storeRecords`, opening the store for each operation, in production; `*store.Store` in process | `interptest.Records` | `interptest.RunRecordsContract`, against all three |
| `Launcher` ([launch seam](mock-agent.md#the-launch-seam)) | `launch.Process` | `interptest.Script` | `interptest.RunLauncherContract` |
| `Prompter`: `render.Prompt` with the caller's baseline and skill roots | `PromptFunc` over `render.Prompt` | a recording `PromptFunc` | none: the real renderer runs in tests |
| `Outbox`: the send half of `messaging.Mailbox` | `cli.storeRecords`, sending through a `messaging.StoreMailbox` over the store it opens for each send, in production; `messaging.StoreMailbox` in process | `messagingtest.Fake` | `messagingtest.RunMailboxContract`, against all three |
| `Rebases`: [rebase-aware review](#rebase-aware-review) | `git.Program` | `gittest.Script` | `gittest.RunClassifierContract` |
| `Forge`: [publish and deliver](#publish-and-deliver) | `forge.GitHub` | `forgetest.Fake` | `forgetest.RunForgeContract` |

`Records` is the first narrow store interface: it lists only the methods the engine calls, as D9 and the [contract rules](contracts.md#which-boundaries-get-an-interface) allow.
The engine does not read workflow approvals; `task new` [enforces them at task creation](configuration.md#enforcement-at-task-creation).
`Engine.Workflow` returns the definition a task's revision names, and the engine refuses one whose name or digest differs (`ErrRevision`).
`Engine.Config` gives each profile's adapter and each repository's path, remote and merge policy; a step without a profile runs `Engine.DefaultAdapter`.
`Engine.StandardsRevision` must name the central standards revision presented for human reviews.
A blank revision refuses to open a human review decision.
The engine persists that revision, the subject, and (for code review) the accepted-spec digest before opening the decision, so later configuration changes cannot change its provenance.

## Worker-stalled observations

Configure `Engine.StallInterval` to a positive duration to enable heartbeat observations; nonpositive values disable them.
`task start` and `step done` set it from the config's [`stall_interval`](configuration.md#settings), 15 minutes unless the config says otherwise, so the observation is on in production.
`Engine.Now` supplies the clock in tests and defaults to wall time.
When an advance reaches a launched, unfinished attempt with no result, the engine reads the launcher's [`heartbeat.json`](artisan.md#heartbeat) from the attempt directory beside `launched.json` with `artisan.ReadHeartbeat`; a heartbeat within the interval (and not in the future) plus `last_output_at` older than the interval sends a `worker-stalled` Director message.
Its derived identity includes the attempt and last-output time: replay and repeated advances send nothing new, while fresh output followed by another crossing sends again.
Attempts, their records and waits are unchanged.
Missing or stale heartbeats, and one naming another attempt, emit nothing; a stale heartbeat of a live launcher is reconcile's [`stuck`](artisan.md#reconcile).
A heartbeat file the engine cannot accept, such as a newer schema, fails the advance with that error rather than being guessed at.
There is no seam: like `launched.json` and `result.json`, the file is read from the attempt directory, and tests write it with `artisan.WriteHeartbeat`.

## Events

| Event | Validated against the records | Recorded |
|---|---|---|
| `task-created` | The task exists and is open. | Nothing by itself. |
| `attempt-result` | The named attempt of the named step exists. | When it is still running: its result file, read with `adapter.ReadResult` for identity `<task>`, `<step>-<n>`; a succeeded result's outputs as documents; then the outcome. When its launcher already recorded the outcome, that outcome stands, and a succeeded one has its outputs recorded by the next advance, whatever its event. |
| `decision-answered` | The named decision exists and is answered. | Nothing by itself: the answer is already recorded. |

Any other event, or one that does not match the records, is refused with `ErrEvent` and changes nothing.

## Advance

After intake, the engine walks the steps in order, from the records, and stops at the first one that must be waited for.

- **Artisan step.** With no attempt yet, the engine checks the profile's adapter with `CheckAdapter` and resolves the inputs, then records the attempt's [binding](#rounds-and-loops) and attempt `n`, [prepares](#attempt-preparation) its prompt in `<state>/<repo>/<task>/<step>-<n>/prompt` and launches it with its result file at `<step>-<n>/result.json`; the task waits for the attempt.
  A succeeded attempt completes the step while it is current; a review step's outcome must also not block (see [rounds and loops](#rounds-and-loops)).
  A failed or stopped attempt waits for the Director, who is told through the outbox.
- **Guildmaster step.** The engine opens decision `<step>-<round>` with the question and options `render.Question` gives, bound to its subject, and waits.
  An answer counts only while its subject is unchanged; when the subject moved, the next round is asked.
  An `approved` answer completes the step; a Guildmaster approval then records the approved document as the accepted spec, `<step>-<round>-spec`.
  On approval, both human review kinds persist the shared `ReviewOutcome` as `<step>-<round>-outcome`, with reviewer `human` and the revisions captured when the decision opened.
  File and record writes are replay-safe.
- **Publish.** The engine pushes the head to the task branch and records its pull request; see [publish and deliver](#publish-and-deliver).
- **Deliver.** The [review gate](#review-gate) is checked first.
  Under the `approval` merge policy the engine then opens the merge decision `deliver-<round>`, kind `merge`, with options `merge` and `hold`, bound to the head, and waits; a head that moved since the last merge decision opens the next round.
  The answer is acted on as in [publish and deliver](#publish-and-deliver); the `auto` policy is refused with `ErrUnsupported`.

A `rejected` outcome, or any Guildmaster answer other than `approved` and `changes_requested`, fails with `ErrUnsupported`.

| Decision | Kind | Subject |
|---|---|---|
| `guildmaster-approval` | `approval` | The `spec` input document's digest |
| `document-review` | `review` | The `subject` input document's digest |
| `review` | `review` | The code head on the `head` input |
| `deliver` | `merge` | The code head on the `head` input |
| loop exit of any looping step | `loop-exit` | The subject of the round that exhausted `max` or raised a dispute |

Every decision the engine opens is also sent to the Director's inbox as message `<repo>.<task>.decision.<id>`, type `decision`, and every failed or stopped attempt as `<repo>.<task>.attempt-failed.<step>-<n>`, type `attempt-failed`.
Publishing and merging send `published`, `merge-held`, `merge-refused` and `merged` messages, described below.

## Publish and deliver

Guildhall prescribes only the edges of delivery: what is pushed where, the pull request, and merging under the repository's merge policy without bypassing the forge's protection ([decision](decisions/delivery-stage.md)).
Inside its task copy an Artisan uses git as it likes, and the Director decides how to recover from anything the forge refuses.
Both steps call the `Forge` seam, whose contract is [`internal/forge`](contracts/forge.md); with no `Engine.Forge`, publishing and merging are refused with `ErrUnsupported`.

**Publish** reads its `head` input.
When the step's latest round already published that head, the step is complete and nothing is called.
Otherwise round `k` (1, or one more than the latest) calls `Forge.Publish` with the repository's configured `path` and `remote`, branch `guildhall/<task>`, the head, the task's title and a body naming the task, step and head.
The forge pushes the head to that branch, replacing whatever the task branch held but never touching another branch, and opens a pull request against the remote's default branch or updates the open one.
A head that is not a commit of the repository at `path` is refused with `forge.ErrUnknownHead` and records nothing.
The engine then records, in order:

1. `publish-<k>-pr` (`tasks/<repo>/<task>/publish-<k>/pull-request.json`), a `guildhall.pull-request/1` document: the remote, number, URL, branch, head and state;
2. message `<repo>.<task>.published.publish-<k>`, type `published`, naming the head, branch and pull request URL;
3. `publish-<k>-head`, the step's output port, the head as a line of text.

A new head, such as a build round after review findings, publishes the next round and updates the same open pull request.

**Deliver** finds the pull request through its `head` input: when that input is a publish step's output, the merge decision's question names the pull request URL.
A `hold` answer merges nothing: the task waits for the Director (`director`, naming the decision) and the Director is told as `<repo>.<task>.merge-held.<decision>`.
A `merge` answer calls `Forge.Merge` for the recorded remote and pull request number at the decision's head, which squash-merges it only while the pull request is still at that head, never with `--admin` or `--auto`.
A head no publish step pushed cannot be merged and is refused with `ErrUnsupported`.
When the forge merges, the engine records `<decision>-merge` (`tasks/<repo>/<task>/<decision>/merge.json`), the merged pull request with its merge commit, sends `<repo>.<task>.merged.<decision>`, type `merged`, marks the task `done` and returns the wait `done`.
A later advance of a done task is refused with `ErrTaskClosed`.

When the forge refuses, such as required checks failing or pending, the branch behind its base, branch protection or the pull request having moved, nothing is recorded: the task waits for the Director and the Director is sent `<repo>.<task>.merge-refused.<decision>.<digest>`, type `merge-refused`, with the forge's reason, where `<digest>` is the first 12 hex digits of the reason's sha256, so a repeated refusal for the same reason sends nothing new.
There is no automatic rebase or retry: each later advance, such as `task start` once the Director has dealt with the reason, asks the forge to merge again.
Any other forge error fails the advance and records nothing.

Replay follows the general rule: a crash before `publish-<k>-head` publishes the same head again, and the forge returns the same pull request; a crash after the forge merged but before `<decision>-merge` asks again, and the forge returns the merge it already made.

## Rounds and loops

Before recording attempt `n` of an Artisan step, the engine records its binding as document `<step>-<n>-inputs` (`tasks/<repo>/<task>/<step>-<n>.inputs.json`): every input it is given, by port, path and digest, and for a review or document review the revisions its outcome must be bound to (subject, accepted-spec digest for code, `Engine.StandardsRevision`).
Besides its wired inputs, an attempt may be given:

- `resolution`: see [rebase-aware review](#rebase-aware-review);
- `previous`: a review step's own latest outcome, whose findings the new outcome must disposition;
- `rulings`: the Director's [rulings](#director-rulings) on findings of a review step's previous outcome, recorded first as document `<step>-<n>-rulings` (`tasks/<repo>/<task>/<step>-<n>.rulings.json`), a JSON array of rulings;
- `feedback`: each blocking result that sent the task back to this step and that no earlier attempt of it was given: an Artisan's outcome document, or the Guildmaster's exact answer recorded as `<decision>-answer` (`tasks/<repo>/<task>/<decision>/answer.md`).

A succeeded attempt stays current while its wired inputs, and for a review its revisions, equal what the step would be given now and no feedback is pending; otherwise the next attempt starts.
So when a looped build lands a new head, every review after it runs again on that head.

A result is **blocking** when it is a `changes_requested` outcome with at least one finding that cites a rule, or a Guildmaster's `changes_requested` answer.
Findings without a rule are suggestions: an outcome requesting changes with only those completes the step (`docs/review.md`).
Each blocking result of a step is one round of its `on.changes_requested` rule; the step may loop `max` rounds, plus one for every loop exit answered `continue`.

- **Within `max`.** The rule's target, which must be an Artisan step, runs again with the result as `feedback`.
  When the target has run and the step's inputs did not change, as when a build returns the same head, the step runs again on the same subject with its previous outcome.
- **Past `max`.** The engine opens decision `<step>-x<round>`, kind `loop-exit`, bound to the round's subject, with options `continue` (one more round), `accept` and `reject`.
  `accept` on a review step records the Guildmaster's approval as `<step>-x<round>-outcome`, an outcome by reviewer `human` bound to the originating review's recorded revisions, and completes the step.
  It retains that review's findings with `still-open` dispositions, so acceptance does not erase findings or claim they were fixed, and later rounds must disposition them.
  The revisions come from the originating human decision's binding or Artisan outcome, never from the configuration at answer-processing time, so stale acceptance cannot satisfy the delivery gate.
  On a `guildmaster-approval` step, `accept` records `<step>-x<round>-spec`, checked against the exit decision's document subject by the spec gate.
  Output resolution orders an exit output immediately after its originating round, before later rounds, for all downstream consumers.
  `reject` leaves the task waiting for the Director, told as `<repo>.<task>.loop-exit-rejected.<id>`.
- **No rule.** The task waits for the Director, told as `<repo>.<task>.changes-requested.<step>-<n>` (or `.<decision>`).

When an attempt's review outcome arrives, it is refused with `ErrOutcome`, recording nothing, if it is not bound to the revisions in its binding (a stale outcome) or if it gives no disposition for a finding id of the previous outcome it was given.

### Director rulings

A review step's attempt is given the [rulings](tasks.md#director-rulings) captured in its recorded `rulings` input on its previous outcome.
Review starts resume an existing immutable binding before creating extras; launch and contest processing both consume that binding, so later rulings cannot enter a replayed attempt.
When its outcome keeps a ruled finding `still-open`, the attempt contests that ruling: the engine records it once, before the attempt is finished, naming the attempt.
A contested ruling on a rule-cited finding of a blocking outcome is a dispute: the engine opens that round's loop exit `<step>-x<round>`, even within `max`, and neither the loop's target nor the step runs until it is answered.
The question identifies the head or document and includes compact finding/rule/move details and references to persisted outcomes and ruling records for complete evidence.
Questions are bounded to the store text limit; long fields and, for very large questions, remaining details are abbreviated.
`continue` upholds the finding and the loop goes on, with one more round allowed; `accept` and `reject` are as at any loop exit.
A finding the attempt withdraws or resolves leaves its ruling uncontested.

### Stall signals

When a review round within its allowed loop count (`max` plus answered `continue` exits) requests changes again, the engine compares its outcome with the previous one and sends the Director message `<repo>.<task>.stall.<step>-<n>`, type `stall`, naming every signal found: the head (or document) is unchanged, no finding was resolved or withdrawn, or a finding is still open for two rounds (report §5.3).
A stall is information only: it never changes the loop or itself asks the Guildmaster; loop exits follow `max` or a [dispute](#director-rulings).

## Review gate

Before `deliver`, every `review` and `document-review` step earlier in the workflow must have a latest recorded outcome, `<step>-<n>-outcome` or a loop exit's `<step>-x<round>-outcome`, that is neither blocking nor `rejected` and is bound (`ReviewOutcome.BoundTo`) to its current document subject or the head being delivered, the step's applicable accepted-spec digest and the current `Engine.StandardsRevision`; otherwise `deliver` is refused with `ErrReviewGate`.
So several review stages, such as the Guildhall repository's three reviews on distinct profiles, must all approve the same head.
A Guildmaster review answered under an older standards revision does not satisfy the gate; asking it again is later work.

## Rebase-aware review

An Artisan code review (`review`) attempt's binding records the commit range it reviews, `Engine.Base`'s answer for the task's repository and the attempt's code head, and its scope.
A pending [full-review request](tasks.md#full-review), read from records through the history view, makes the attempt full without classifying: the attempt consumes it with `Records.ConsumeFullReview`, as `<step>-<n>`, before recording its binding, and a replay finds it consumed by the same attempt and stays full.
`Engine.Base` is supplied by the caller; when it is nil, fails or returns something other than a full commit id, the attempt records no range and its scope is `full`.
The range last reviewed is read only from records, through the history view: the binding of the attempt that recorded the step's latest outcome; a loop exit's outcome, or an attempt that recorded no range, gives none.
Only when the previous outcome's binding has revisions matching the new attempt's accepted-spec digest and standards revision is its range eligible for reuse; missing revisions or a change to either field retains full scope without classification.
When there is an eligible last range with a different head, `Engine.Rebases` classifies the new range against it ([`internal/git`](contracts/git.md)); only `scope: rebase` narrows the round.
The resolution diff is then recorded first as document `<step>-<n>-resolution` and given on port `resolution`, beside the `previous` outcome, and the binding records `scope: rebase`.
A full classification or a nil `Engine.Rebases` keeps full scope; a classifier error instead aborts the advance before recording the binding or launching the attempt.
When replaying preparation before a binding was recorded, a recorded resolution diff whose digest no longer matches the classification keeps full scope.
Once the binding is recorded, replay reuses it without reclassifying.
The outcome still binds to the attempt's head, spec digest and standards revision, so the review gate is unchanged.

## Documents and inputs

A succeeded result's outputs are copied from the task's working directory into the Director document directory at `tasks/<repo>/<task>/<step>-<n>/<output path>` and recorded with their sha256.
Output reads are rooted in the working directory: file and parent-directory symlinks cannot escape it.
All outputs, including supplemental ones, are read and checked before any are copied or recorded.
The first output is the step's output port, named `<step>-<n>-<port>`; later outputs add `-<index>`, from 2.
A code-head output must hold a 40 or 64 lowercase hex object id, and a review outcome must parse, or nothing is recorded.
Every review outcome, including human review and loop-exit acceptance, projects findings and dispositions in its document reference for inspection.
An input `step.port` resolves to that port's document of the latest round, and its file is checked against the recorded digest before it is used.
The working directory is `<state>/<repo>/<task>/work` until worktree custody is defined.

## History

Every read of where a record sits in a task's history goes through one private view in `internal/interp` (`history.go`), built from the task's records at the start of an advance and updated by each write the advance makes, so a read after a write sees it.
It answers a step's latest attempt, its round decisions and next round, its loop exits and how many were answered `continue`, its blocking results in round order, the originating round of each exit, which feedback an attempt was given, the latest document of a step's output port and the range a code review's latest outcome reviewed.
Record names stay the keys and keep their forms (`<step>-<n>`, `<step>-x<k>`, `<step>-<n>-<port>`, `<step>-<n>-<port>-<index>`, `<step>-<n>-inputs`, `<decision>-revisions`, `<decision>-answer`); they are parsed in one place, from the right, so a step id ending in `-<digits>` or `-x<digits>` is never mistaken for a round of a step it extends.
Exit `k` of a step originates in its `k`th blocking result; an exit's output orders immediately after that round and before later rounds.
A missing or unreadable record the view needs, such as the outcome an exit originated in, is an error, never a guess that the step approved.
The view is private: it is not a storage interface and has no double; it runs over both the store and `interptest.Records` through the engine tests.

## Spec gate

Every input of type accepted spec, such as `build`'s and `review`'s `spec`, must come from an approval decision answered `approved` whose subject digest equals the accepted spec's digest; otherwise the step is refused with `ErrSpecGate` before anything is recorded.
Recording the accepted spec checks the same: the approved document must be the one the decision was opened for.

## Replay and recovery

Every write uses a mutation id derived from the task and the record, such as `attempt:<repo>:<task>:<step>:<n>`, `attempt-finish:…`, `document:<repo>:<task>:<name>` and `decision:<repo>:<task>:<id>`, and messages use derived ids too.
The engine derives its position from the records, not from the event, so repeating any advance after a crash at any point replays its writes and records nothing twice.
An advance is several writes, one per record, each committed on its own; the ordering (outputs before the attempt's outcome, the attempt before its launch) makes every partial advance one that a repeat completes.
Once `Launch` returns, the engine marks the attempt directory with `launched.json`.
An attempt recorded but neither marked launched nor holding a result is launched again with the same identity from its published preparation, never recorded again and never rendered again once prepared.
A lost acknowledgement, or a crash after `Launch` returns but before the mark, relaunches the same identity with the same files; refusing a live duplicate belongs to the launcher's ownership record.
An attempt's binding is recorded before the attempt, and a relaunch uses the recorded binding, so a replayed launch is given exactly the inputs of the first.
Result intake publishes `intake.json` atomically in the attempt directory only after every output document, ruling contest and stall notification succeeds and the attempt outcome is recorded or reconciled.
A launcher-recorded success without this marker replays the intake operations idempotently; the first output document alone is not completion evidence.
If the launcher concurrently records the same outcome, the store's [first finish stands](store.md#attempts) and intake's finish returns the launcher's receipt; a different persisted outcome stands and the advance reports an error.
A Guildmaster's answer is recorded as a document before the attempt it feeds, so a repeat completes it.
`Advance` is not safe for concurrent use on one task; callers must serialise whole advances, as the [CLI's task lock](running.md#what-an-advance-runs-over) does.

### Attempt preparation

A missing `launched.json` does not prove that no worker holds the attempt's files, so an attempt's preparation is published once and never replaced ([decision](decisions/0036-immutable-attempt-preparation.md)).
A fresh attempt is rendered into `<step>-<n>/prompt.partial`, its manifest is written there as `manifest.json`, and the directory is renamed to `<step>-<n>/prompt` before launch.
Replay validates any published directory rather than relying on rename atomicity or crash durability across platforms.
No launch is given a path inside `prompt.partial`, so a leftover one from a crash is removed and rendered again.
A replay that finds `prompt/` launches from it unchanged, with the manifest it holds, after checking that:

- `manifest.json` is a `guildhall.prompt-manifest/1` document read with [`strictjson`](contracts/strictjson.md), so with no unknown or duplicate fields at any depth and no trailing JSON values, naming this attempt's repository, task, step, kind, kind version and attempt number;
- its inputs are exactly the attempt's recorded binding, by port, path and digest;
- `prompt.md`, every input copy and every skill file match their manifest digests, each skill's tree digest matches its files, and `prompt/` holds no other files.

Preparation that fails a check is refused with `ErrPreparation`: nothing is launched, removed or rewritten.
A newer manifest also matches `strictjson.ErrNewerSchema`, which tells a Guildhall too old to read it from a damaged preparation.
Changes to the role, guide, standards, skills or inputs after preparation reach only a later attempt, which has its own identity.

## Tests

`deliver_test.go`, red first against the engine that refused `publish` (`publish steps run by the engine`; see the [interpreter contract tests](contracts/interp.md#tests) for the two cases that were not red), runs `testdata/workflows/publish.json`, the straight workflow with a publish step, over `forgetest.Fake` on both backends: publish pushes `guildhall/t7` and records the pull request and head, the merge question names the pull request, a `merge` answer merges it, records `deliver-1-merge` and closes the task, `hold` and a forge refusal wait for the Director with their messages and a later advance retries a refused merge, a missing forge or an unknown head records nothing, an unpublished head cannot be merged, and a failure before or after each of the three records replays to one pull request and the right number of merges.

`internal/interp/engine_test.go` runs the straight workflow, `testdata/workflows/straight.json` (plan, Guildmaster acceptance, build, Artisan review, Guildmaster sign-off, deliver), twice: against the scripted mock with the in-memory records and `messagingtest.Fake`, and against the same mock with a real store file and its mailbox.
It checks each event's effect on the records, the approval bound to the plan document's digest, the spec gate, a crash between recording and launching an attempt, the zero-model guard, a failed attempt, mismatched events and a changed revision.
`prepare_test.go` checks [attempt preparation](#attempt-preparation): a lost acknowledgement or lost `launched.json` with a changed baseline and skill tree replays the same files and spec, a refused live duplicate leaves every byte and path intact, invalid preparation is refused with `ErrPreparation`, and a worker started from the test binary reads its original input after the replay.
`loops_test.go` runs the round trace of report §7.1 with three code reviews on distinct profiles (`testdata/workflows/three-reviews.json`) against both backends, and checks, mostly against both, a stale outcome refused, a missing disposition refused, stall signals on an unchanged head, `max` opening the loop exit with `continue`, `accept` and `reject`, a Guildmaster's changes looping to the plan, changes without a rule and the review gate.
Human-review regression coverage includes plan → human document review → approval, code-review provenance, and replay after a failed outcome-record write.
Output containment tests cover escaping file symlinks, parent-directory symlinks, and supplemental outputs.
`rulings_test.go` checks [Director rulings](#director-rulings) on both backends: a ruling given to the next review, withdrawn or contested once, and a contested blocker opening the loop exit within `max`.
See the [interpreter contract tests](contracts/interp.md#tests) for history-view and per-write replay coverage.
`TestRecordsContract` runs `interptest.RunRecordsContract` against the store and the double, including concurrent list readers and record writers.
