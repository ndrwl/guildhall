# Interpreter

`internal/interp` advances a task through the workflow revision it bound when it was created.
`interp.Engine.Advance(ctx, event)` is deterministic: it derives workflow progress from the task's store records, never from prompt instructions or the Director's say-so, and makes no model call.
It follows `changes_requested` and `failed` loop rules with their `max`, asks the Guildmaster at a loop's exit, checks the review and validation gates before `deliver`, [publishes and merges](#publish-and-deliver) through the forge and [validates](#validate) the published head against the forge's checks; it hands [Director stages](#director-stages) to the Director; the checks gate, automatic merging and loops back to a step not run by an Artisan are refused with `ErrUnsupported`.
[`task start`, `step done`, `step finish` and `step retry`](running.md) run it from the command line; from a copy, each attempt's launcher runs them itself as the task's [watcher](running.md#the-watcher).

## Seams

The engine consumes seven narrow interfaces, declared in `internal/interp` beside the code that calls them.

| Seam | Real implementation | Double | Shared suite |
|---|---|---|---|
| `Records`: read a task, its attempts, documents, decisions and rulings; start and finish attempts, record documents, open decisions and contest rulings | `cli.storeRecords`, opening the store for each operation, in production; `*store.Store` in process | `interptest.Records` | `interptest.RunRecordsContract`, against all three |
| `Launcher` ([launch seam](mock-agent.md#the-launch-seam)) | `launch.Process` | `interptest.Script` | `interptest.RunLauncherContract` |
| `Prompter`: `render.Prompt` with the caller's baseline and skill roots | `PromptFunc` over `render.Prompt` | a recording `PromptFunc` | none: the real renderer runs in tests |
| `Outbox`: the send half of `messaging.Mailbox` | `cli.storeRecords`, sending through a `messaging.StoreMailbox` over the store it opens for each send, in production; `messaging.StoreMailbox` in process | `messagingtest.Fake` | `messagingtest.RunMailboxContract`, against all three |
| `Rebases`: [rebase-aware review](#rebase-aware-review) | `git.Program` | `gittest.Script` | `gittest.RunClassifierContract` |
| `Forge`: [publish and deliver](#publish-and-deliver), [validate](#validate) | `forge.GitHub` | `forgetest.Fake` | `forgetest.RunForgeContract` |
| `Workspaces`: [the working directory](#the-working-directory) | `git.Program` | `gittest.Workspaces` | `gittest.RunWorkspaceContract` |

`Records` is the first narrow store interface: it lists only the methods the engine calls, as D9 and the [contract rules](contracts.md#which-boundaries-get-an-interface) allow.
The engine does not read workflow approvals; `task new` [enforces them at task creation](configuration.md#enforcement-at-task-creation).
`Engine.Workflow` returns the definition a task's revision names, and the engine refuses one whose name or digest differs (`ErrRevision`).
`Engine.Config` gives each profile's adapter and each repository's path, remote and merge policy; an Artisan step without a profile is refused before admission or advancement, with a remedy to set a profile on that step in a custom workflow.
`Engine.StandardsRevision` supplies the central standards revision presented for human reviews, following the [review outcome revision contract](configuration.md#review-outcome).
The engine persists that revision, the subject, and (for code review) the accepted-spec digest before opening the decision, so later configuration changes cannot change its provenance.

## Plan refutation

The optional `refute` kind uses the ordinary Artisan launch and document-intake path, with a fresh attempt reading `spec` and optional research `context`, then recording `<step>-<n>-findings`.
The [baseline stage guide](../baseline/stages/refute.md) owns the critique instructions; the generated required-output instructions name `findings.md` and include it in the result template.
Intake enforces the existing document contract, not the semantic quality or severity labels of the prose; deterministic tests make no model calls.
When findings are supplied to acceptance, the question lists both spec and refutation by artifact name and digest.
Acceptance stays bound to the spec digest and copies only the approved spec to its accepted-spec output; refutation cannot authorize a build or satisfy a review gate.
A `changes_requested` acceptance with an existing bounded rule back to plan re-runs subsequent refutation against the new spec; there is no refute-specific loop or automatic revision.
See [configuration](configuration.md#opt-in-plan-refutation) for opt-in wiring and trial guidance.

## Worker-stalled observations

Configure `Engine.StallInterval` to a positive duration to enable heartbeat observations; nonpositive values disable them.
`task start`, `step done` and `step retry` set it from the config's [`stall_interval`](configuration.md#settings), 15 minutes unless the config says otherwise, so the observation is on in production.
`Engine.Now` supplies the clock in tests and defaults to wall time.
When an advance reaches a launched, unfinished attempt with no result, the engine reads the launcher's [`heartbeat.json`](artisan.md#heartbeat) from the attempt directory beside `launched.json` with `artisan.ReadHeartbeat`; a heartbeat within the interval (and not in the future) plus `last_output_at` older than the interval sends a `worker-stalled` Director message.
Its derived identity includes the attempt and last-output time: replay and repeated advances send nothing new, while fresh output followed by another crossing sends again.
`interp.StalledMessage(key, lastOutput)` is that message, id, type and text; the attempt's launcher sends the same message from its own heartbeat when it runs with [`--stall-interval`](artisan.md#heartbeat), which `launch.Process` passes from `Engine.StallInterval`, so a silent worker is reported without anything advancing its task, and an advance observing the same crossing replays it.
The advance's observation stays as the fallback, for a launcher that runs without the interval or could not send.
`interp.AdvanceFailedMessage(key, command, lastErr)` is the other message a launcher sends, `advance-failed`, once per attempt, when its [watcher](running.md#the-watcher) advance failed twice.
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
| `attempt-retried` | The named attempt of the named step exists, finished `failed` or `stopped`, and is the step's latest. When the step's latest attempt is the next one, the event is a replay. | The step's next attempt, started as a first attempt starts; once it has launched, the Director's reason, when given, as message `<repo>.<task>.attempt-retried.<step>-<n>`, type `attempt-retried`. A replay launches the next attempt if it was recorded but not launched, and records nothing but that message, sent again when it is missing. |

Any other event, or one that does not match the records, is refused with `ErrEvent` and changes nothing.
So `attempt-retried` is refused for a running or succeeded attempt, for one a later attempt of the step follows other than its retry, and for an unknown step or attempt; a closed task is refused with `ErrTaskClosed`, as for every event.

## Advance

After intake, the engine walks the steps in order, from the records, and stops at the first one that must be waited for.

- **Artisan step.** With no attempt yet, the engine checks the profile's adapter with `CheckAdapter` and resolves the inputs, then records the attempt's [binding](#rounds-and-loops) and attempt `n`, [prepares](#attempt-preparation) its prompt in `<state>/<repo>/<task>/<step>-<n>/prompt` and launches it with its result file at `<step>-<n>/result.json`; the task waits for the attempt.
  A succeeded attempt completes the step while it is current; a review step's outcome must also not block (see [rounds and loops](#rounds-and-loops)).
  A failed or stopped attempt waits for the Director, who is told through the outbox; the Director's `attempt-retried` for it starts the next attempt exactly as a first attempt starts: inputs bound again, prompt prepared, launched and marked `launched.json`.
  Attempts are not capped (R1.34): each retry is the Director's decision.
- **Director stage.** A step whose actor is `director` is recorded, bound and prepared as an Artisan step is, then handed to the Director instead of launched, and the task waits with `director-stage`; see [Director stages](#director-stages).
- **Guildmaster step.** The engine opens decision `<step>-<round>` with the question and options `render.Question` gives, bound to its subject, and waits.
  An answer counts only while its subject is unchanged; when the subject moved, the next round is asked.
  An `approved` answer completes the step; a Guildmaster approval then records the approved document as the accepted spec, `<step>-<round>-spec`.
  On approval, both human review kinds persist the shared `ReviewOutcome` as `<step>-<round>-outcome`, with reviewer `human` and the revisions captured when the decision opened.
  File and record writes are replay-safe.
- **Publish.** The engine pushes the head to the task branch and records its pull request; see [publish and deliver](#publish-and-deliver).
- **Validate.** The engine reads the forge's checks of the published head and waits while they are pending; see [validate](#validate).
- **Deliver.** The [review gate](#review-gate) and the [validation gate](#validate) are checked first.
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
| `audit`, after a sampled merge | `audit` | The head that merged |

Every decision the engine opens is also sent to the Director's inbox as message `<repo>.<task>.decision.<id>`, type `decision`, every Director stage handed to it as `<repo>.<task>.stage.<step>-<n>`, type `director-stage`, and every failed or stopped attempt as `<repo>.<task>.attempt-failed.<step>-<n>`, type `attempt-failed`.
Every attempt starts under `Engine.Caps`, the [attempt caps](director.md#guard-rails); one refused at a cap records and launches nothing, sends `<repo>.<task>.budget-refused.<YYYY-MM-DD>`, type `budget-refused`, once per UTC day, naming `step retry` for a refused retry and `task start` otherwise, and fails the advance with an error wrapping `store.ErrAttemptCap` that names the rail's setting.
Publishing, validating and merging send `published`, `checks-pending`, `checks-passed`, `checks-failed`, `merge-held`, `merge-refused`, `merged` and `workspace-kept` messages, described below.

## Director stages

A Director stage is a step whose `actor` setting is `director`, such as the default workflow's `grill`: the Director runs it in its own session, in conversation with the Guildmaster, and ends it with a recorded result ([decision](decisions/0072-director-stages.md)).
The engine runs it as an attempt, so everything after the attempt's result is the Artisan path unchanged:

1. With no attempt yet, the engine resolves the inputs (no adapter is checked), records the attempt's binding and attempt `n` with `StartAttempt`, makes the [working directory](#the-working-directory) ready and [prepares](#attempt-preparation) `<step>-<n>/prompt` through `Prompter`, which renders the Director's role (`roles/director.md`, or the config's `[director] role`), the step's stage guide, its `skills` and its inputs.
2. It launches nothing.
   It sends the Director message `<repo>.<task>.stage.<step>-<n>`, type `director-stage`, naming the step, the prompt's path, the kind's output ports and the `step finish` command that ends the stage; the CLI's outbox wakes the Director for it, once, as for every Director message.
3. It then writes `launched.json` as `{"launcher":"director","identity":{...}}`, with no process, and returns `{"wait":"director-stage","step":<step>,"attempt":<n>}`.

A crash between recording the attempt and the mark replays the hand-off from the published preparation: nothing is rendered again, and the message, sent again under the same id, is a replay that neither stores nor wakes twice.
An advance that finds the stage marked and without a result waits again, sending nothing; there is no heartbeat, so no `worker-stalled` observation.
An advance that finds the stage's result published but not taken, as a `step finish` that stopped after writing it leaves, takes it as `attempt-result` would, whatever its event: no launcher announces a Director stage's result, so `task start` recovers it.
`interp.HandedToDirector(dir)` reports whether an attempt directory's `launched.json` names the director launcher.

The Director ends the stage with [`step finish`](running.md#step-finish), which writes the attempt's `result.json` with `adapter.WriteResult` and feeds `attempt-result`, so the intake, the output documents (`<step>-<n>-<port>`), the outcome and replay are an Artisan's.
A stage the Director abandons is finished `--failed`: the task waits for the Director with `attempt-failed`, as after a failed Artisan attempt, and `step retry` hands attempt `n+1` to the Director in the same way.

## Publish and deliver

Guildhall prescribes only the edges of delivery: what is pushed where, the pull request, and merging under the repository's merge policy without bypassing the forge's protection ([decision](decisions/0060-delivery-stage.md)).
Inside its task copy an Artisan uses git as it likes, and the Director decides how to recover from anything the forge refuses.
Both steps call the `Forge` seam, whose contract is [`internal/forge`](contracts/forge.md); with no `Engine.Forge`, publishing and merging are refused with `ErrUnsupported`.

**Publish** reads its `head` input.
When the step's latest round already published that head, the step is complete and nothing is called.
Otherwise round `k` (1, or one more than the latest) calls `Forge.Publish` with the repository's configured `path` and `remote`, branch `guildhall/<task>`, the head, the task's title and a body naming the task, step and head, followed for a task created with `--fixes` by "Fixes a defect introduced by task `<repository>/<task>`.", the line the [quality rubric](quality.md)'s rework and change failure count from.
The forge pushes the head to that branch, replacing whatever the task branch held but never touching another branch, and opens a pull request against the remote's default branch or updates the open one.
A head that is not a commit of the repository at `path` is refused with `forge.ErrUnknownHead` and records nothing; a commit an Artisan made in the task's [working directory](#the-working-directory) is one, because that directory is a worktree of the repository.
The engine then records, in order:

1. `publish-<k>-pr` (`tasks/<repo>/<task>/publish-<k>/pull-request.json`), a `guildhall.pull-request/1` document: the remote, number, URL, branch, head and state;
2. message `<repo>.<task>.published.publish-<k>`, type `published`, naming the head, branch and pull request URL;
3. `publish-<k>-head`, the step's output port, the head as a line of text.

A new head, such as a build round after review findings, publishes the next round and updates the same open pull request.

**Deliver** finds the pull request through its `head` input: when that input is a publish step's output, the merge decision's question names the pull request URL.
A `hold` answer merges nothing: the task waits for the Director (`director`, naming the decision) and the Director is told as `<repo>.<task>.merge-held.<decision>`.
A `merge` answer calls `Forge.Merge` for the recorded remote and pull request number at the decision's head, which squash-merges it only while the pull request is still at that head, never with `--admin` or `--auto`.
A head no publish step pushed cannot be merged and is refused with `ErrUnsupported`.
When the forge merges, the engine records `<decision>-merge` (`tasks/<repo>/<task>/<decision>/merge.json`), the merged pull request with its merge commit, sends `<repo>.<task>.merged.<decision>`, type `merged`, [retires the task's working directory](#the-working-directory), marks the task `done` and returns the wait `done`.
Before marking it `done`, when `quality_audit_sample` (default 5, 0 off, 1 every task), independently of self-review, [samples](configuration.md#quality-settings) the task for the Guildmaster's audit ([G4](quality.md#honesty-and-evidence-g)), it opens decision `audit`, kind `audit`, bound to the head that merged and naming the pull request, with options `yes` and `no` for its first question, and sends it to the Director like every decision; the wait is still `done`, and answering it records the answer only.
A later advance of a done task is refused with `ErrTaskClosed`.

When the forge refuses, such as required checks failing or pending, the branch behind its base, branch protection or the pull request having moved, nothing is merged.
The engine records the refusal as `<decision>-refused`, then `<decision>-refused-2` and on (`tasks/<repo>/<task>/<decision>/refused-<digest>.json`, where `<digest>` is the first 12 hex digits of the document's sha256), holding schema `guildhall.merge-refusal/1`, the remote, pull request number and URL, the head and the forge's reason; a refusal whose content one of the decision's refusals already holds is not recorded again, so a repeated or replayed refusal for the same reason is recorded once.
The task then waits for the Director and the Director is sent `<repo>.<task>.merge-refused.<decision>.<digest>`, type `merge-refused`, with the forge's reason, where `<digest>` is the first 12 hex digits of the reason's sha256, so a repeated refusal for the same reason sends nothing new.
There is no automatic rebase or retry: each later advance, such as `task start` once the Director has dealt with the reason, asks the forge to merge again.
Any other forge error, such as a forge it could not reach, fails the advance, records nothing and sends no `merge-refused` message.

Replay follows the general rule: a crash before `publish-<k>-head` publishes the same head again, and the forge returns the same pull request; a crash after the forge merged but before `<decision>-merge` asks again, and the forge returns the merge it already made.

## Validate

Validate reads its `head` input, which must be a publish step's output: a head no publish step pushed is refused with `ErrUnsupported`, and so is validating with no `Engine.Forge` ([decision](decisions/0066-validate-stage.md)).
When the step's latest result is for that head and `passed`, the step is complete and nothing is called.
Otherwise each advance calls `Forge.Checks` for the repository's `path`, the remote the head was published to and the head, the commit's checks rather than whatever its pull request is at now:

- **Pending.** Some check is pending, none has failed, or no check is reported yet: nothing is recorded and the task waits with `{"wait":"checks","step":"<step>"}`.
  The Director is told once per head as `<repo>.<task>.checks-pending.<step>.<first 12 hex digits of the head>`, type `checks-pending`.
  A repository configured [`no_ci`](configuration.md#repositories) has no checks to wait for: there, no check reported is finished and passes, with the result marked `"no_ci": true` and the `checks-passed` message saying the head has no CI; checks the forge does report still count.
- **Finished.** Every check has passed, or at least one has failed even while others are pending: round `k` (1, or one more than the latest) records `<step>-<k>-result` (`tasks/<repo>/<task>/<step>-<k>/result.json`), the step's output port, a `guildhall.validation-result/1` document holding the remote, pull request number and URL, the head, the verdict `passed` or `failed`, every check with its name, state, URL and the forge's id of that run of the check, sorted as the forge returned them, the names of the failed ones as `failing`, and `no_ci` when set.
  The Director is told as `<repo>.<task>.checks-passed.<step>-<k>` or `.checks-failed.<step>-<k>`, naming the head, the pull request and the failing checks; each advance sends the latest result's message again, so one lost to a crash after its result was recorded is sent by the replay.

A `passed` result completes the step.
A `failed` result is a blocking result of the step's `on.failed` [loop rule](#rounds-and-loops): within `max` the rule's target, which must be an Artisan step, runs again with the result as `feedback`; the new head is then reviewed, published and validated in the next round.
Past `max` the loop exit `<step>-x<round>`, bound to the head and naming the failing checks, offers only `continue` and `reject`: red checks cannot be accepted.
Without a rule, the task waits for the Director (`director`, naming the step).

**Waiting on a failed head.** Three states leave the step waiting on the checks of a head whose latest result failed: no `on.failed` rule (`director`), a loop exit answered `reject` (`director`, naming the decision), and a loop target that ran and left the head unchanged (`checks`).
In each, an advance reads the head's checks again, so a check the Director re-ran counts.
A read records a new round only when it is new evidence: a pass, or a failed check run, by check name and run id, that is not among the latest result's failed runs, which is a new failure or a re-run that failed again.
Failed runs that are all among the latest result's, the same ones or fewer, such as while one failed check re-runs or after it passes and another stays failed, keep the step waiting and spend no round of `max`, so a build that leaves the head unchanged launches nothing more until a check fails anew or everything passes.
A pass completes the step; a new failure follows the loop rule as any failed round does, so after a rejected exit it opens the next loop exit.
A forge that gives no run ids cannot show a re-run that failed the same way, which then reads as unchanged.

**Pacing.** `Engine.ChecksRefresh`, the config's [`checks_refresh`](configuration.md#settings) (default one minute), paces the reads: once a validate step has read a head's checks, an advance reads that head's checks again only after `ChecksRefresh` has passed, and otherwise returns the same wait without calling the forge.
The time of the last read is kept in `<state>/<repo>/<task>/<step>.checks.json`; a new head is read at once, and zero reads at every advance.
Nothing in the engine reads the checks in the background: an advance does, such as `task start`, or the Director session supervisor's [checks watcher](director.md#checks).

The **validation gate**: when `deliver`'s `validation` input is wired, its document must be a `passed` result for the head being delivered, or `deliver` is refused with `ErrValidationGate`.
Because validate only completes on a passed result for its current head, the gate holds whenever the walk reaches `deliver`; it is kept deliberately as defence in depth, as the review gate is, and guards a workflow and records that disagree.

## Rounds and loops

Before recording attempt `n` of an Artisan step, the engine records its binding as document `<step>-<n>-inputs` (`tasks/<repo>/<task>/<step>-<n>.inputs.json`): every input it is given, by port, path and digest, and for a review or document review the revisions its outcome must be bound to (subject, accepted-spec digest for code, `Engine.StandardsRevision`).
New review preparations pass that binding's exact revisions to `render.Stage.Revisions` and its range base to `render.Stage.ReviewBase`, leaving the latter empty when no range was recorded.
The [review outcome contract](configuration.md#review-outcome) owns revision validity and exact matching, including unconfigured standards.
The base is not resolved again during preparation, including rebase-only and requested-full rounds.
The [rendering contract](rendering.md#artisan-prompts) owns the emitted field, and [attempt preparation](#attempt-preparation) owns published-prompt immutability, including prompts prepared before this field existed.
Besides its wired inputs, an attempt may be given:

- `resolution`: see [rebase-aware review](#rebase-aware-review);
- `previous`: a review step's own latest outcome, whose findings the new outcome must disposition;
- `rulings`: the Director's [rulings](#director-rulings) on findings of a review step's previous outcome, recorded first as document `<step>-<n>-rulings` (`tasks/<repo>/<task>/<step>-<n>.rulings.json`), a JSON array of rulings;
- `feedback`: each blocking result that sent the task back to this step and that no earlier attempt of it was given: an Artisan's outcome document, or the Guildmaster's exact answer recorded as `<decision>-answer` (`tasks/<repo>/<task>/<decision>/answer.md`).

Feedback a failed or stopped attempt was given is still pending, so the attempt that retries it is given it again.
A succeeded attempt stays current while its wired inputs, and for a review its revisions, equal what the step would be given now and no feedback is pending; otherwise the next attempt starts.
So when a looped build lands a new head, every review after it runs again on that head.

A result is **blocking** when it is a `changes_requested` outcome with at least one finding that cites a rule, or a Guildmaster's `changes_requested` answer.
Findings without a rule are suggestions: an outcome requesting changes with only those completes the step (`docs/review.md`).
A `validate` step's `failed` result is blocking too, and is one round of its `on.failed` rule ([validate](#validate)).
Each blocking result of a step is one round of its `on.changes_requested` rule (`on.failed` for validate); the step may loop `max` rounds, plus one for every loop exit answered `continue`.

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

## Code-head output intake

A succeeded attempt with one output port of type `code-head` and no listed output files takes its output from Git HEAD in the task's working directory.
This applies to build and any other attempt producing that port type, not to document or review outputs.
Guildhall records the full commit object id followed by a newline as `<step>-<n>-<port>`, in the document directory at `tasks/<repo>/<task>/<step>-<n>/head`; it writes no head file in the worktree.
Tracked staged or unstaged changes, including submodules, refuse intake with an error naming the dirty paths, rather than silently leaving work out of the output.
Untracked files alone do not block it.
An unborn HEAD, missing repository or Git failure is an intake error.
An explicitly listed head file keeps the existing path and content validation.
The launcher may already have recorded success: while `intake.json` is absent the next advance still takes the result, records the head and proceeds without retrying the succeeded attempt.
A head document already recorded wins on replay even if the working directory has since changed.
The task's custody lock serialises advances; external writers must not change the worktree during intake.

## Review gate

Before `deliver`, every `review` and `document-review` step earlier in the workflow must have a latest recorded outcome, `<step>-<n>-outcome` or a loop exit's `<step>-x<round>-outcome`, that is neither blocking nor `rejected` and is bound (`ReviewOutcome.BoundTo`) to its current document subject or the head being delivered, the step's applicable accepted-spec digest and the current `Engine.StandardsRevision`; otherwise `deliver` is refused with `ErrReviewGate`.
So several review stages, such as the Guildhall repository's three reviews on distinct profiles, must all approve the same head.
A Guildmaster review answered under an older standards revision does not satisfy the gate; asking it again is later work.

## Rebase-aware review

An Artisan code review (`review`) attempt's binding records the commit range it reviews, `Engine.Base`'s answer for the task's repository and the attempt's code head, and its scope.
A pending [full-review request](tasks.md#full-review), read from records through the history view, makes the attempt full without classifying: the attempt consumes it with `Records.ConsumeFullReview`, as `<step>-<n>`, before recording its binding, and a replay finds it consumed by the same attempt and stays full.
`Engine.Base` is supplied by the caller, as the CLI supplies it from `git.Program.Base` ([what an advance runs over](running.md#what-an-advance-runs-over)); when it is nil, fails or returns something other than a full commit id, the attempt records no range and its scope is `full`.
The range last reviewed is read only from records, through the history view: the binding of the attempt that recorded the step's latest outcome; a loop exit's outcome, or an attempt that recorded no range, gives none.
Only when the previous outcome's binding has revisions matching the new attempt's accepted-spec digest and standards revision is its range eligible for reuse; missing revisions or a change to either field retains full scope without classification.
When there is an eligible last range with a different head, `Engine.Rebases` classifies the new range against it ([`internal/git`](contracts/git.md)); only `scope: rebase` narrows the round.
The resolution diff is then recorded first as document `<step>-<n>-resolution` and given on port `resolution`, beside the `previous` outcome, and the binding records `scope: rebase`.
A full classification or a nil `Engine.Rebases` keeps full scope; a classifier error instead aborts the advance before recording the binding or launching the attempt.
When replaying preparation before a binding was recorded, a recorded resolution diff whose digest no longer matches the classification keeps full scope.
Once the binding is recorded, replay reuses it without reclassifying.
The outcome still binds to the attempt's head, spec digest and standards revision, so the review gate is unchanged.

## Documents and inputs

A succeeded result's listed output files are copied from the task's working directory into the Director document directory at `tasks/<repo>/<task>/<step>-<n>/<output path>` and recorded with their sha256.
An omitted code-head output follows [code-head output intake](#code-head-output-intake) instead.
Output reads are rooted in the working directory: file and parent-directory symlinks cannot escape it.
All outputs, including supplemental ones, are read and checked before any are copied or recorded.
The first output is the step's output port, named `<step>-<n>-<port>`; later outputs add `-<index>`, from 2.
A code-head output must hold a 40 or 64 lowercase hex object id, and a review outcome must parse, or nothing is recorded.
Every review outcome, including human review and loop-exit acceptance, projects findings and dispositions in its document reference for inspection.
An input `step.port` resolves to that port's document of the latest round, and its file is checked against the recorded digest before it is used.

## The working directory

Every Artisan attempt of a task runs in one working directory, which a Director stage's outputs are also copied into, `<state>/<repo>/<task>/work`, a git worktree of the repository's configured `path` ([decision](decisions/0068-worktree-custody.md)).
Before each launch the engine calls `Workspaces.Ensure`, whose contract is [`internal/git`](contracts/git.md#workspaces), with that path, the repository's `path` and `remote`, and branch `guildhall/<task>`, the branch publish pushes to:

- the first attempt creates the worktree on a new local branch `guildhall/<task>` at the commit the remote's default branch is at then, or on an existing local branch of that name as it is;
- every later attempt, including a retry, a loop round and a relaunch, reuses it unchanged: Guildhall never resets, cleans or checks out over it, so an Artisan uses git freely inside it and its commits, branches and uncommitted files carry to the next attempt;
- listed output files sit beside the code; an Artisan that commits should leave them out.
  An omitted code-head output is recorded outside the worktree ([code-head output intake](#code-head-output-intake)).

Ensure is replay safe: the worktree is built beside the working directory and moved into place only when complete, so a crash leaves no working directory or a complete one, and the attempt, recorded but not marked launched, is launched by the next advance.
When the working directory cannot be made, such as when the remote is unreachable or the directory holds files but is not a worktree of the repository, the advance fails before launching anything and running it again retries; a plain working directory an earlier Guildhall made for a task that is still open is refused this way rather than replaced.
When a merge closes the task, the engine calls `Workspaces.Retire` with the merged head and the task's recorded documents, before marking the task `done`, so a replay retires it again.
It removes the worktree and deletes the local branch `guildhall/<task>` only when nothing in them would be lost: no changed or staged tracked file, no untracked file other than a recorded output with the same bytes, and a worktree HEAD and branch tip that are the merged head or behind it.
Otherwise it removes nothing, and the Director is sent `<repo>.<task>.workspace-kept.<decision>.<digest>`, type `workspace-kept`, naming the directory, the branch and what they hold, where `<digest>` is the first 12 hex digits of the text's sha256; the Director decides what happens to them.
A retirement that fails, such as git refusing to remove the worktree, is reported the same way and never fails the merge or keeps the task open.
A retirement cut short by an interrupt is not: the advance fails before the task is marked `done`, no `workspace-kept` message is sent, and running the command again replays the merge and retires the working directory again.
With no `Engine.Workspaces` the working directory is a plain directory, which is how the engine's own tests run; publish cannot push from one.

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
An attempt recorded but neither marked launched nor holding a result is launched again with the same identity from its published preparation, subject to the [recovery hold](contracts/interp.md#contract), never recorded again and never rendered again once prepared.
A lost acknowledgement, or a crash after `Launch` returns but before the mark, relaunches the same identity with the same files; refusing a live duplicate belongs to the launcher's ownership record.
An attempt's binding is recorded before the attempt, and a relaunch uses the recorded binding, so a replayed launch is given exactly the inputs of the first.
Result intake publishes `intake.json` atomically in the attempt directory only after every output document, ruling contest and stall notification succeeds and the attempt outcome is recorded or reconciled.
A launcher-recorded success without this marker replays the intake operations idempotently; the first output document alone is not completion evidence.
This includes attempts left succeeded by an older release whose intake failed: the next `task start` or `step done` validates them again.
Missing required outputs, invalid output paths, unreadable files named by the result, invalid port content and stale or undispositioned review outcomes reject intake.
Malformed or wrong-identity result bytes also reject intake; a missing or unreadable result file and a newer unsupported result version remain operational errors.
Rejection records the attempt as `failed` with `intake_rejection`, using a separate [`reject-attempt` mutation](store.md#attempts), rather than attempting another finish over the launcher's success.
The task waits `{"wait":"director","step":"<step>","attempt":<n>}` and sends one `attempt-failed` message naming the attempt and rejection reason.
The Director can then use `step retry` normally; the rejected result file is retained for diagnosis.
The stored failure, not an intake marker, drives rejection recovery: a crash before notification replays the message without duplicating it.
Store failures, interrupts, inability to open the working directory, Guildhall-owned binding or document I/O errors, and code-head derivation failures are not output rejection: the advance returns an error and unfinished intake remains replayable.
If the launcher concurrently records the same outcome, the store's [first finish stands](store.md#attempts) and intake's finish returns the launcher's receipt; outside the explicit intake-rejection operation, a different persisted outcome stands and the advance reports an error.
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

`deliver_test.go`, red first against the engine that refused `publish` (`publish steps run by the engine`; see the [interpreter contract tests](contracts/interp.md#tests) for the two cases that were not red), runs `testdata/workflows/publish.json`, the straight workflow with a publish step, over `forgetest.Fake` on both backends: publish pushes `guildhall/t7` and records the pull request and head, the merge question names the pull request, a `merge` answer merges it, records `deliver-1-merge` and closes the task, `hold` and a forge refusal wait for the Director with their messages, a forge it cannot reach fails the advance without a refusal or message, a refusal is recorded as `deliver-1-refused` once per reason (`deliver-1-refused-2` for a new reason, nothing for a repeated one) and a later advance retries a refused merge, a missing forge or an unknown head records nothing, an unpublished head cannot be merged, and a failure before or after each of the three records replays to one pull request and the right number of merges.

`validate_test.go`, red first against the engine that refused `validate` (`validate steps run by the engine`; see the [interpreter contract tests](contracts/interp.md#tests) for the case that was not red), runs `testdata/workflows/validate.json`, plan through an Artisan review, publish, validate with `on.failed` back to build at most once, and deliver, over `forgetest.Fake`: no checks and pending checks wait with one `checks-pending` message per head and record nothing, passed checks record `validate-1-result` and open the merge decision, failed checks record a failed result and start build 2 with it as `feedback` and its next head is reviewed, published and validated as round 2, a failed round past `max` opens `validate-x2` with `continue` and `reject`, a build that leaves the head unchanged has its checks read again and spends a round only on a newly failed run, failed checks without a rule and a rejected exit wait for the Director and read a re-run pass, no checks under `no_ci` pass, `ChecksRefresh` paces the reads, a forge that cannot read checks fails the advance and records nothing, and a failure before or after recording a passed or failed result replays to one result, its message and one launch of build 2.
`director_stage_test.go` runs `testdata/workflows/director-grill.json`, a Director grill before research and the straight path, on both backends: the grill is recorded, bound and prepared with the Director's role and its skills, marked launched by the Director and handed over with one `director-stage` message and no launch; a repeated advance, or one after a crash before the mark, renders and sends nothing new; the Director's result records `grill-1-notes` and moves the task to research, and the plan gets both notes as `context`; a failed stage waits for the Director and `step retry` hands attempt 2 over; a Director stage after an Artisan stage is handed over in the advance that takes the Artisan's result.
`TestDefaultWorkflowDelivers` runs the shipped default workflow, its grill overridden to an Artisan with no skills, on both backends from task creation through grill, research, plan, acceptance, build, review, publish, a failed validation looping to build, the new head's review, publish and passed validation, to the merge and the closed task.

`internal/interp/engine_test.go` runs the straight workflow, `testdata/workflows/straight.json` (plan, Guildmaster acceptance, build, Artisan review, Guildmaster sign-off, deliver), twice: against the scripted mock with the in-memory records and `messagingtest.Fake`, and against the same mock with a real store file and its mailbox.
It checks each event's effect on the records, the approval bound to the plan document's digest, the spec gate, a crash between recording and launching an attempt, the zero-model guard, a failed attempt, mismatched events and a changed revision.
`prepare_test.go` checks [attempt preparation](#attempt-preparation): a lost acknowledgement or lost `launched.json` with a changed baseline and skill tree replays the same files and spec, a refused live duplicate leaves every byte and path intact, invalid preparation is refused with `ErrPreparation`, and a worker started from the test binary reads its original input after the replay.
`loops_test.go` runs the round trace of report §7.1 with three code reviews on distinct profiles (`testdata/workflows/three-reviews.json`) against both backends, and checks, mostly against both, a stale outcome refused, a missing disposition refused, stall signals on an unchanged head, `max` opening the loop exit with `continue`, `accept` and `reject`, a Guildmaster's changes looping to the plan, changes without a rule and the review gate.
Human-review regression coverage includes plan → human document review → approval, code-review provenance, and replay after a failed outcome-record write.
Output containment tests cover escaping file symlinks, parent-directory symlinks, and supplemental outputs.
`rulings_test.go` checks [Director rulings](#director-rulings) on both backends: a ruling given to the next review, withdrawn or contested once, and a contested blocker opening the loop exit within `max`.
See the [interpreter contract tests](contracts/interp.md#tests) for history-view and per-write replay coverage.
`TestRecordsContract` runs `interptest.RunRecordsContract` against the store and the double, including concurrent list readers and record writers.
