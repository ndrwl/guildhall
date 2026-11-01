# Interpreter

`internal/interp` advances a task through the workflow revision it bound when it was created.
`interp.Engine.Advance(ctx, event)` is deterministic: it derives workflow progress from the task's store records, never from prompt instructions or the Director's say-so, and makes no model call.
It follows `changes_requested` and `failed` loop rules with their `max`, asks the Guildmaster at a loop's exit, checks the review and validation gates before `deliver`, [publishes and merges](#publish-and-deliver) through the forge and [validates](#validate) the published head against the forge's checks; it hands [Director stages](#director-stages) to the Director; the checks gate, `changes_requested` loops back to a step run by neither an Artisan nor the Director, and `failed` loops back to a step not run by an Artisan are refused with `ErrUnsupported`.
[`task start`, `step done`, `step finish`, `step retry` and `step send-back`](running.md) run it from the command line; from a copy, each attempt's launcher runs them itself as the task's [watcher](running.md#the-watcher).

## Seams

The engine consumes eight narrow interfaces, declared in `internal/interp` beside the code that calls them.

| Seam | Real implementation | Double | Shared suite |
|---|---|---|---|
| `Records`: read a task, its attempts, documents, decisions, rulings, task context, task links, task overrides, dependency records and dependency override; start and finish attempts, record documents, open decisions and contest rulings; optionally, as `MergeQueue`, read and change its repository's [merge queue](#the-merge-queue) | `cli.storeRecords`, opening the store for each operation, in production; `*store.Store` in process | `interptest.Records` | `interptest.RunRecordsContract`, against all three |
| `Launcher` ([launch seam](mock-agent.md#the-launch-seam)) | `launch.Process` | `interptest.Script` | `interptest.RunLauncherContract` |
| `Prompter`: `render.Prompt` with the caller's baseline and skill roots | `PromptFunc` over `render.Prompt` | a recording `PromptFunc` | none: the real renderer runs in tests |
| `Outbox`: the send half of `messaging.Mailbox` | `cli.storeRecords`, sending through a `messaging.StoreMailbox` over the store it opens for each send, in production; `messaging.StoreMailbox` in process | `messagingtest.Fake` | `messagingtest.RunMailboxContract`, against all three |
| `Rebases`: [rebase-aware review](#rebase-aware-review) | `git.Program` | `gittest.Script` | `gittest.RunClassifierContract` |
| `Forge`: [publish and deliver](#publish-and-deliver), [validate](#validate) | `forge.GitHub` | `forgetest.Fake` | `forgetest.RunForgeContract` |
| `MergeabilityReader`: [mergeability observations](#mergeability-observations), optional on the forge | `forge.GitHub` | `forgetest.Fake` | `forgetest.RunMergeabilityContract` |
| `Workspaces`: [the working directory](#the-working-directory), `Ensure`, `FastForward`, `Behind` and `Retire`; [the head check](#the-head-check), `Tip`, `IsAncestor` and `Base`; [tiered rebase](#tiered-rebase), `Rebase`, `Squash`, `Adopt` and `Diff` | `git.Program` | `gittest.Workspaces` | `gittest.RunWorkspaceContract` |

`Records` is the first narrow store interface: it lists only the methods the engine calls, as [bbolt for the store](decisions/bbolt-for-the-store.md) and the [contract rules](contracts.md#which-boundaries-get-an-interface) allow.
The engine does not read workflow approvals; `task new` [enforces them at task creation](configuration.md#enforcement-at-task-creation).
`Engine.Workflow` returns the definition a task's revision names, and the engine refuses one whose name or digest differs (`ErrRevision`); the CLI resolves it by the revision's name, not the repository's current workflow setting ([running](running.md#what-an-advance-runs-over)).
`Engine.Config` gives each profile's adapter and each repository's path, remote and merge requirements, read at every advance; an Artisan step runs under the profile it names or else its [role profile](configuration.md#profiles) (`workflow.EffectiveProfile`), and one that resolves no declared or shipped profile is refused before admission or advancement, with a remedy to set a profile on that step.
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
| `step-sent-back` | The [send-back admission and replay requirements](step-send-back.md). | Immutable `send-back-<step>-<attempt>` feedback document, then the step's next attempt under ordinary admission gates; changed-feedback replays are refused. |
| `design-requested` | A nonempty brief, and the [design round](#design-rounds) refusals: the task is a feature task, build has not started, no round is open, no attempt runs and the grill has succeeded. A brief equal to the latest round's is a replay while that round is open. | Immutable `<decision>-<n>-brief` document for round `n`, then the round's designer attempt under ordinary admission gates; a replay records nothing. A refusal is `ErrDesignRound` and records nothing. |
| `attempt-retried` | The named attempt of the named step exists, finished `failed` or `stopped`, and is the step's latest. When the step's latest attempt is the next one, the event is a replay. | The step's next attempt, started as a first attempt starts, with the retry recorded in its [binding](#rounds-and-loops) so its prompt says why it is a retry; once it has launched, the Director's reason bound to it, when given, as message `<repo>.<task>.attempt-retried.<step>-<n>`, type `attempt-retried`. A replay launches the next attempt if it was recorded but not launched, and records nothing but that message, sent again when it is missing. |

Any other event, or one that does not match the records, is refused with `ErrEvent` and changes nothing.
So `attempt-retried` is refused for a running or succeeded attempt, for one a later attempt of the step follows other than its retry, and for an unknown step or attempt; a closed task is refused with `ErrTaskClosed`, as for every event.

## Advance

After intake, the engine walks the steps in order, from the records, and stops at the first one that must be waited for.
Just before the first step whose actor is not the Director, whatever its kind, it checks the [dependency gate](#the-dependency-gate).

- **Artisan step.** With no attempt yet, the engine checks the profile's adapter with `CheckAdapter` and resolves the inputs, then records the attempt's [binding](#rounds-and-loops) and attempt `n`, [prepares](#attempt-preparation) its prompt in `<state>/<repo>/<task>/<step>-<n>/prompt` and launches it with its result file at `<step>-<n>/result.json`; the task waits for the attempt.
  A succeeded attempt completes the step while it is current; a review step's outcome must also not block (see [rounds and loops](#rounds-and-loops)).
  A failed or stopped attempt waits for the Director, who is told through the outbox; the Director's `attempt-retried` for it starts the next attempt exactly as a first attempt starts: inputs bound again, prompt prepared, launched and marked `launched.json`, with the retry also bound so the prompt opens with why it is a retry.
  Attempts are not capped (R1.34): each retry is the Director's decision.
- **Director stage.** A step whose actor is `director` is recorded, bound and prepared as an Artisan step is, then handed to the Director instead of launched, and the task waits with `director-stage`; see [Director stages](#director-stages).
- **Guildmaster step.** The engine opens decision `<step>-<round>` with the question and options `render.Question` gives, bound to its subject, and waits.
  An answer counts only while its subject is unchanged; when the subject moved, the next round is asked.
  An `approved` answer completes the step; a Guildmaster approval then records the approved document as the accepted spec, `<step>-<round>-spec`.
  On approval, both human review kinds persist the shared `ReviewOutcome` as `<step>-<round>-outcome`, with reviewer `human` and the revisions captured when the decision opened.
  File and record writes are replay-safe.
- **Publish.** The engine pushes the head to the task branch and records its pull request; see [publish and deliver](#publish-and-deliver).
- **Validate.** The engine reads the forge's checks of the published head and waits while they are pending; see [validate](#validate).
- **Deliver.** The [review gate](#review-gate) and the [validation gate](#validate) are checked first, each unless the repository's [merge requirements](configuration.md#merge-requirements) turn it off.
  When `guildmaster_approval` is required, or another requirement fails, the engine then opens the merge decision `deliver-<round>`, kind `merge`, with options `merge` and `hold`, bound to the head, and waits; a head that moved since the last merge decision opens the next round.
  The answer is acted on as in [publish and deliver](#publish-and-deliver), which also describes the merge Guildhall makes by itself.
  An authorized merge joins its repository's [merge queue](#the-merge-queue), and only the front is brought up to date and merged.

A `rejected` outcome, or any Guildmaster answer other than `approved` and `changes_requested`, fails with `ErrUnsupported`.

| Decision | Kind | Subject |
|---|---|---|
| `guildmaster-approval` | `approval` | The `spec` input document's digest |
| `document-review` | `review` | The `subject` input document's digest |
| `review` | `review` | The code head on the `head` input |
| `deliver` | `merge` | The code head on the `head` input |
| loop exit of any looping step | `loop-exit` | The subject of the round that exhausted `max` or raised a dispute |
| `audit`, after a sampled merge, opened only by releases before the audit was retired | `audit` | The head that merged |

Every decision the engine opens is also sent to the Director's inbox as message `<repo>.<task>.decision.<id>`, type `decision`, every Director stage handed to it as `<repo>.<task>.stage.<step>-<n>`, type `director-stage`, and every failed or stopped attempt as `<repo>.<task>.attempt-failed.<step>-<n>`, type `attempt-failed`.
Every Artisan attempt starts under `Engine.Caps`, the [attempt caps](director.md#guard-rails), and a Director stage's through `Records.StartDirectorAttempt`, held but never capped or queued.
One the running cap does not admit is [queued](#the-start-queue).
One refused at the daily cap records and launches nothing, sends `<repo>.<task>.budget-refused.<YYYY-MM-DD>`, type `budget-refused`, once per UTC day, naming `step retry` for a refused retry and `task start` otherwise, and fails the advance with an error wrapping `store.ErrAttemptCap` that names the rail's setting.
Publishing, validating and merging send `published`, `pr-blocked`, `checks-pending`, `checks-passed`, `checks-failed`, `merge-held`, `merge-refused`, `merged` and `workspace-kept` messages, and the merge queue and its rebases `merge-queued`, `rebased`, `rebase-escalated`, `rebase-stuck` and `approval-carried`, described below.

## The start queue

A new Artisan attempt the running cap does not admit, because `max_running_attempts` attempts run or another start is ahead in the store's start queue, waits `queued` ([decision](decisions/guard-rails.md)).
The engine passes `AttemptCaps.Queue`, the request the start is made under: `retry` with the retried attempt when the walk follows a retry of the step, and otherwise `start` for `task-created`, `decision` for `decision-answered`, `send-back` for `step-sent-back` and `engine` for an attempt result.
`Records.StartCappedAttempt` then records the step's queue entry and no attempt and returns a `*store.StartQueued`, and the advance returns `Next{Wait: WaitQueued, Step, Attempt, Position}`: no error, nothing launched and no message.
The binding is recorded before the capped start, as for any start, so the queued attempt keeps its inputs, a retry's first bound reason among them.

A later advance that reaches the step starts it once it is the queue's front and a slot is free, or returns `queued` again.
For a retry the walk's own rule would not get there, since a failed or stopped latest attempt waits for the Director: a latest attempt `n-1` that failed or stopped with a queue entry for attempt `n` counts as an owed retry, as a matching `attempt-retried` does, so a plain `task-created` advance, such as the supervisor's [start-queue watcher](director.md#the-start-queue)'s, starts it and sends its `attempt-retried` reason once it launches.
A send-back's feedback stays pending and an answered decision is read from the records, so both resume on a plain advance already.
A retry of another attempt while one is queued is refused with `ErrEvent`, naming the queued attempt and its place.

## The dependency gate

A task with dependencies does not run research, or any later step, until every task it depends on is `done`, that is, merged (R1.14, R1.58).
Its dependencies are its [effective dependencies](tasks.md#changing-dependencies): the `depends_on` it was created with, with the Director's later `task depends add|remove` records applied in order (`store.EffectiveDependencies`), read through `Records.TaskDependencies`.
The gate stands in the walk over the steps, just before the first step whose actor is not `director`, so it holds an Artisan, Guildmaster or engine step alike, and also a send-back or retry, which pass through the same walk.
Director stages, such as the grill, are never held: the grill, its `step finish` and the watcher's advance after it run while dependencies are open.

The gate passes when any of these is true, each read from the records:

1. The task already has an attempt of a step not run by the Director. Nothing is read. A task already past research is unaffected, and a loop, send-back or retry back to research is never held again; a dependency added to it holds nothing.
2. The task has no effective dependencies. Only its dependency records, usually none, are read, and no dependency is looked up, so a task without dependencies behaves as before.
3. The Director recorded a [dependency override](tasks.md#dependencies) for the task, which lasts for the rest of its life, so a dependency added after it holds nothing either.
4. Every effective dependency's task state is `done`, read through `Records.Task`, which reads a task of any repository in the same store.

Otherwise the task is held: the advance records nothing, and returns `{"wait":"dependencies","step":<step>}` for the held step (`WaitDependencies`).
The gate runs before the step itself, so before the [pause hold](director.md#pause-and-resume), the caps, preparation and any record: a held task never reaches them.

While held, each advance sends the Director one message, type `dependency-held`, listing every unmerged dependency as `<repo>/<id>: <state>`, in repository then id order, followed by the override command.
The state is coarse, read from the dependency's records, so a dependency moving through its normal steps sends nothing new:

- `cancelled; it can never merge; start anyway with guildhall task start --ignore-deps --reason <reason> --repo <r> <t>`;
- `waiting for the Guildmaster: decision <id> open`;
- `waiting for the Director: <step> attempt <n> <failed|stopped>`, for a step whose latest attempt failed or stopped;
- `queued for merge`, for a dependency whose authorized merge waits in its repository's [merge queue](#the-merge-queue);
- `merge held`, for one whose merge is held with [`task hold-merge`](tasks.md#holding-a-merge);
- `in progress` otherwise.

Its id is `<repo>.<task>.dependency-held.<hex>`, where `<hex>` is the first 12 hex digits of the sha256 of the state lines: the same states send the same message again, which is a replay and sends nothing, and a change sends a new one.
This is not the between-Directors [`dependency-notice`](messaging.md#message-types).

The gate only holds or releases; it moves no branch.
A released task still starts research on the main its dependencies merged into, because every Artisan attempt's launch [fast-forwards](#the-working-directory) an untouched task branch first, research's first attempt included.

Nothing advances a held task from inside its dependency's own advance, which would take a second task lock inside the first.
The session supervisor's [dependency watcher](director.md#dependencies) advances held tasks with no model turn, and `director status` lists each as a `dependencies` item.

## Director stages

A Director stage is a step whose `actor` setting is `director`, such as the default workflow's `grill`: the Director runs it in its own session, in conversation with the Guildmaster, and ends it with a recorded result ([decision](decisions/engine-advance.md)).
The engine runs it as an attempt, so everything after the attempt's result is the Artisan path unchanged:

1. With no attempt yet, the engine resolves the inputs (no adapter is checked), records the attempt's binding and attempt `n` with `StartAttempt`, makes the [working directory](#the-working-directory) ready and [prepares](#attempt-preparation) `<step>-<n>/prompt` through `Prompter`, which renders the Director's role (`roles/director.md`, or the config's `[director] role`), the step's stage guide, its `skills` and its inputs.
2. It launches nothing.
   It sends the Director message `<repo>.<task>.stage.<step>-<n>`, type `director-stage`, naming the step, the prompt's path, the kind's output ports and the `step finish` command that ends the stage; the CLI's outbox wakes the Director for it, once, as for every Director message.
3. It then writes `launched.json` as `{"launcher":"director","identity":{...}}`, with no process, and returns `{"wait":"director-stage","step":<step>,"attempt":<n>}`.

A crash between recording the attempt and the mark replays the hand-off from the published preparation: nothing is rendered again, and the message, sent again under the same id, is a replay that neither stores nor wakes twice.
An advance that finds the stage marked and without a result waits again, sending nothing; there is no heartbeat, so no `worker-stalled` observation.
An advance that finds the stage's result published but not taken, as a `step finish` that stopped after writing it leaves, takes it as `attempt-result` would, whatever its event: no launcher announces a Director stage's result, so `task start` recovers it.
`interp.HandedToDirector(dir)` reports whether an attempt directory's `launched.json` names the director launcher.

A `changes_requested` [loop](#rounds-and-loops) may go back to a Director stage: its next attempt is handed over the same way, its binding and prompt carrying `feedback`, `previous` and `reviewed`.

The Director ends the stage with [`step finish`](running.md#step-finish), which writes the attempt's `result.json` with `adapter.WriteResult` and feeds `attempt-result`, so the intake, the output documents (`<step>-<n>-<port>`), the outcome and replay are an Artisan's.
A stage the Director abandons is finished `--failed`: the task waits for the Director with `attempt-failed`, as after a failed Artisan attempt, and `step retry` hands attempt `n+1` to the Director in the same way.

A design round's `changes_requested` also goes back to the grill, through the same path; see [design rounds](#design-rounds).

## Design rounds

A design round is a design the Director asks for with [`task design`](tasks.md#design-rounds), on a feature task, after its grill has succeeded and before its first `build` attempt.
It is a Director tool, not a workflow step: no workflow names it, and the shipped workflows and their digests do not change.
Each accepted request is one round, whatever its outcome; the rounds used are counted from the round briefs, never from a stored counter.
There is no cap: how many rounds to run is the Director's call.

**Feature tasks.** A task records no kind, so the rule is read from its bound workflow: it must have a `grill` step run by the Director and a `build` step.
The shipped bug workflow has no grill, so a bug task is refused with "bugs have no design round".

**Step ids.** A round's decisions use step id `design`, so round `n`'s decision is `design-<n>`; its designer's Artisan attempts use `designer`, so attempt `k` is `designer-<k>` and its output document `designer-<k>-design`.
Attempt and round numbers drift apart after a retry, so the two never share a document prefix.
When the bound workflow already has a step `design` or `designer`, both ids are prefixed with `round-`, again until neither is taken: the choice depends on the bound workflow alone, so it is stable for the life of the task, and no workflow is refused (`interp.DesignSteps`).

**The request.** `design-requested` records the brief as the immutable document `<decision>-<n>-brief`, at `tasks/<repo>/<task>/<decision>-<n>/brief.md`, before anything launches; a request whose brief equals the latest round's is a replay and records nothing while that round is open; once its decision is answered, the same brief is a new request for the next round.
It is refused with `ErrDesignRound`, recording nothing, for a task that is not a feature task, once any `build` attempt exists ("build has started"), while a round is open (its decision is not answered), while any attempt of the task runs, and until the grill's latest attempt has succeeded and has served every design answer that reopened it.
A done or cancelled task is refused with `ErrTaskClosed`, as for every event.

**Running the round.** Before the walk, every round answered `approved` has its design recorded, once, as `<decision>-<n>-design`.
While the latest round is open, the advance starts no workflow step:

1. With no designer attempt bound to the round's brief, the [dependency gate](#the-dependency-gate) is checked for the designer as for any Artisan attempt, then the [pause hold](director.md#pause-and-resume); the designer's attempts never count as passing the gate, so a design never lets research past it.
   The attempt is bound and started as the rebaser's synthetic attempts are: under the attempt caps, in the [start queue](#the-start-queue) under request `design`, and under the task lock, with the shipped `designer` profile, at effort `high`, and the `design` stage guide.
   Its inputs are `brief` and, as `context`, the latest grill notes, research notes and plan spec, in workflow order, when they exist, then the task's context.
2. While the designer runs, the advance waits for it, relaunching one recorded but never launched, as for any attempt; a failed or stopped designer waits for the Director (`attempt-failed`), and `step retry` of it starts the next designer attempt in the same round.
3. Once the designer succeeds, the engine opens decision `<decision>-<n>`, kind `approval`, options `approved` and `changes_requested`, bound to the design's digest, and waits; `task start --decision <decision>-<n>` moves the task on once it is answered.

**The answer.** `approved` closes the round: plan's binding gains the latest approved design on the engine-added `design` port.
It is the one engine-added port [staleness](#rounds-and-loops) counts, and only for a `plan` step: a plan that already succeeded becomes stale, runs again with the design, and its acceptance is asked again on the new spec's digest: an acceptance still open on the old spec is voided as superseded, and the next round is asked on the new spec's digest.
The void happens when the walk next reaches the acceptance, once plan has succeeded again, with the reason `Superseded: the spec changed to <digest>; asked again as <step>-<n+1>`; a late `task start --decision` or `decide answer` of the voided round is refused.
No other finished attempt becomes stale, so research is kept; build sees the design only through the spec.
`changes_requested` closes the round and reopens the grill: until a later grill attempt that did not fail or stop has bound it on `feedback`, the answer is the grill's pending feedback, recorded as `<decision>-<n>-answer`, so the grill's next attempt is handed to the Director with `feedback`, `previous` (its latest notes) and `reviewed` (the design), as a [loop to a Director stage](#director-stages) is.
Once the grill finishes, the walk goes on: plan runs again if it had run, because the grill notes changed, and the Director may ask for another round, which is round `n+1`.

A crashed round replays from the records: the designer is found by the brief its binding holds, so it is never launched twice, and the decision is opened once.

## Publish and deliver

Guildhall prescribes only the edges of delivery: what is pushed where, the pull request, and merging under the repository's merge requirements without bypassing the forge's protection ([decision](decisions/delivery-stage.md)).
Inside its task copy an Artisan uses git as it likes, and the Director decides how to recover from anything the forge refuses.
Both steps call the `Forge` seam, whose contract is [`internal/forge`](contracts/forge.md); with no `Engine.Forge`, publishing and merging are refused with `ErrUnsupported`.

**Publish** reads its `head` input, its optional `review` inputs and its optional `spec` input, the accepted spec (the default workflow wires `accept.spec`).
When the step's latest round already published that head, the step is complete and nothing is called.
Otherwise the [review gate](#review-gate) is checked first, over the review steps before publish; then round `k` (1, or one more than the latest) calls `Forge.Publish` with the repository's configured `path` and `remote`, branch `guildhall/<task>`, the head, and a title and body assembled from records Guildhall already holds, with no model call:

- the title is the spec's first `# ` heading, else the task's title, else its id, on one line, neutralised as below and cut to at most 256 characters, the one title rule for every forge request, so a [rebase push](#tiered-rebase), whose title is the task's title, else its id, follows it too;
- the body's sections are `## Summary` (the spec's `## Goal` section, else its first paragraph), `## What changed` (the `summary` of the result of the attempt that recorded the head, read as a retry's is), `## How it was tested` (the spec's first section whose heading mentions testing, quoted, then a line saying validation runs after publish), `## Review` (each review outcome given that is bound to this head, with its findings and dispositions, then the Director's rulings on the task), and `## Guildhall` (the task, step, head and the accepted spec's document name and digest, with the spec inlined in a `<details>` block), each with a stated fallback when its source is missing;
- for a task created with `--fixes`, the body ends with "Fixes a defect introduced by task `<repository>/<task>`.", the line the [quality rubric](quality.md)'s rework and change failure count from.

Spec, build summary and review text are model-written, so `@` mentions and GitHub closing keywords before an issue reference are made inert with a word joiner and any line holding "Fixes a defect introduced by task" is dropped from them.
The body is capped at 24,000 bytes, so it fits Windows's command line, by cutting the inlined spec, then the build summary, then the review list, each cut marked `… (truncated)`; the Guildhall section's names and head and the fix line are never cut.
The text is deterministic, so a replayed round asks the forge for no edit, and every new head rewrites the open pull request's title and body.
The forge pushes the head to that branch, replacing whatever the task branch held but never touching another branch, and opens a pull request against the remote's default branch or updates the open one.
A head that is not a commit of the repository at `path` is refused with `forge.ErrUnknownHead` and records nothing; a commit an Artisan made in the task's [working directory](#the-working-directory) is one, because that directory is a worktree of the repository.
The engine then records, in order:

1. `publish-<k>-pr` (`tasks/<repo>/<task>/publish-<k>/pull-request.json`), a `guildhall.pull-request/1` document: the remote, the title it published (the merge's subject is made from it, [forge contract](contracts/forge.md)), number, URL, branch, head and state;
2. message `<repo>.<task>.published.publish-<k>`, type `published`, naming the head, branch and pull request URL;
3. `publish-<k>-head`, the step's output port, the head as a line of text.

A new head, such as a build round after review findings, publishes the next round and updates the same open pull request.

**Deliver** reads the repository's [merge requirements](configuration.md#merge-requirements) from the current configuration, with any the task's [overrides](tasks.md#task-overrides) turn on (`run.repository`, which every gate reads), and evaluates them in order on every advance that has not yet authorized the delivered head, so an override added later never retracts an authorization ([decision](decisions/delivery-stage.md)):

1. `artisan_approval` and `ci_green`: when one is required and does not hold, the advance fails with `ErrReviewGate` or `ErrValidationGate`, whatever `guildmaster_approval` says, and no answer merges over it; a required `ci_green` does not hold when deliver takes no `validation` input ([validation gate](#validate)), and a required `artisan_approval` does not hold when no `review` or `document-review` step comes before deliver (`deliver step "<id>" takes no review outcome`), even in a Guildmaster-approved workflow the admission floor did not check. When one is not required its gate is skipped, but its state is still reported.
2. The delivered head: the published head, or the head validate [rebased](#tiered-rebase) it to, followed through validate's rebase documents; the review and CI gates of a rebased head are read as below.
3. `nothing_open`: the latest outcomes of the review steps before deliver have no open finding, and the task has no open decision but the deliver step's own; a voided decision, such as one an earlier slice's merge voided or a superseded acceptance, is not open and does not count.
   Every finding is open, unresolved or newly raised, except a `disputed` one with a standing (uncontested) Director ruling of any move on that finding of that document; suggestions never count.
   The reason names the counts, such as "review-3-outcome has 1 unresolved and 1 disputed finding(s)", and adds "N suggestion(s), not counted" when suggestions exist, even when the condition holds, so an authorization records them.
   An approval recorded by a loop-exit `accept` keeps its findings unresolved, so it still holds the merge for the Guildmaster: the accept covered the loop, not the merge.
4. `guildmaster_approval`: when required, the engine opens the merge decision as described above.
   When not required and every other required requirement holds, it records `deliver-<k>-authorized` (`tasks/<repo>/<task>/deliver-<k>/authorized.json`), schema `guildhall.merge-authorization/1`, holding the head, the pull request, the four effective requirements, the mergeability state and each requirement's condition with the names of its evidence (the review outcome documents, the validation result and any unanswered decisions), then merges with no decision under the merge id `deliver-<k>`.
   When not required but `nothing_open` fails, it falls back: it opens the merge decision as if `guildmaster_approval` were required, and the question begins "Guildhall did not merge by itself: " and the failed requirements.
   While that decision is unanswered, every advance evaluates the requirements again; once they all hold, such as after the Director rules on a disputed finding, it records the next `deliver-<k>-authorized`, whose `supersedes` names the unanswered decision, and the merge proceeds without it; the merged message says that decision is no longer needed.
   An answer given before that still merges or holds as below.
5. The [merge queue](#the-merge-queue): the authorized merge joins its repository's queue, and only the front reads mergeability and is merged or [rebased](#tiered-rebase).

The decision opens, or the authorization is recorded, whatever the pull request's mergeability: a behind or conflicting pull request no longer waits for the Director before it.

When that input is a publish step's output, the merge decision's question names the pull request URL.
Its question lists each requirement as `holds`, `fails: <reason>` or `not required` (a holding `nothing_open` adds its suggestion count); a `merge` answer overrides a failing `nothing_open`, and nothing else.
It then says that an authorized merge joins the repository's merge queue and that Guildhall may rebase the head onto the base before it merges.
It ends by listing each finding of the latest review outcomes with its rule and state, and each suggestion as `id: summary`, abbreviated as the store text limit requires.
A merge authorized for a head is reused on replay, so a crash after the authorization merges the same head with no decision, whatever the requirements say by then.
Below, the merge id `<merge>` is the decision's id, or `deliver-<k>` for a merge Guildhall made by itself.
A `hold` answer merges nothing: the task waits for the Director (`director`, naming the decision) and the Director is told as `<repo>.<task>.merge-held.<decision>`.
A `merge` answer, or a merge Guildhall authorized, once the task holds the front of the merge queue and its pull request is clear, calls `Forge.Merge` for the recorded remote and pull request number at the head, which squash-merges it only while the pull request is still at that head, never with `--admin` or `--auto`.
A head no publish step pushed cannot be merged and is refused with `ErrUnsupported`.
When the forge merges, the engine records `<merge>-merge` (`tasks/<repo>/<task>/<merge>/merge.json`), the merged pull request with its merge commit, sends `<repo>.<task>.merged.<merge>`, type `merged`, saying "(decision <merge>)" or "(merged by Guildhall, <merge>-authorized)", [retires the task's working directory](#the-working-directory), voids every decision of the task still open, with reason `Task done: pull request <url> merged`, marks the task `done` and returns the wait `done`.
A `merge` answer can override a failing `nothing_open`, so a question, a loop exit or an earlier round may still be open here; closing voids them all, with no exceptions, so a closed task never has an open decision ([decision](decisions/closed-tasks-have-no-open-decisions.md)).
Every engine close takes this one path (`closeTask`): the last Guildhall merge, a [merge outside Guildhall](tasks.md#merged-outside-guildhall) and a [report-only finish](#report-only-workflows); each void is keyed on the decision and the close's one reason, so a close that fails part-way replays to the same writes.
It opens no decision as it closes the task: the Guildmaster's `audit` ([G4](quality.md#honesty-and-evidence-g)) is retired, and the deprecated `quality_audit_sample` has no effect ([decision](decisions/retire-quality-audit.md)).
An `audit` decision an earlier release opened is voided by the close like any other.
A later advance of a done task is refused with `ErrTaskClosed`.

When the forge refuses, such as required checks failing or pending, the branch behind its base, branch protection or the pull request having moved, nothing is merged.
The engine records the refusal as `<merge>-refused`, then `<merge>-refused-2` and on (`tasks/<repo>/<task>/<merge>/refused-<digest>.json`, where `<digest>` is the first 12 hex digits of the document's sha256), holding schema `guildhall.merge-refusal/1`, the remote, pull request number and URL, the head and the forge's reason; a refusal whose content one of the merge's refusals already holds is not recorded again, so a repeated or replayed refusal for the same reason is recorded once.
The task then waits for the Director and the Director is sent `<repo>.<task>.merge-refused.<merge>.<digest>`, type `merge-refused`, with the forge's reason, where `<digest>` is the first 12 hex digits of the reason's sha256, so a repeated refusal for the same reason sends nothing new.
A refusal is not retried automatically: each later advance, such as `task start` once the Director has dealt with the reason, asks the forge to merge again.
A pull request that is behind or conflicting is not a refusal: the front reads its mergeability before merging and [rebases](#tiered-rebase) it first.
Any other forge error, such as a forge it could not reach, fails the advance, records nothing and sends no `merge-refused` message.

Replay follows the general rule: a crash before `publish-<k>-head` publishes the same head again, and the forge returns the same pull request; a crash after the forge merged but before `<merge>-merge` asks again, and the forge returns the merge it already made.

### Mergeability observations

Before validate reads checks, and before the front of the [merge queue](#the-merge-queue) merges, a forge implementing `MergeabilityReader` reads the PR's `mergeable` and `mergeStateStatus` with `gh pr view <number> --repo <repository> --json url,state,headRefOid,baseRefName,mergeable,mergeStateStatus`.
A conflicting (`CONFLICTING` or `DIRTY`) PR, or a `BEHIND` PR blocked by strict branch protection, is obstructed.
At validate a conflicting PR is [rebased](#tiered-rebase), since GitHub runs no checks on it, and a behind one reads its checks as usual; at deliver the front is rebased.
Only a PR Guildhall cannot rebase there waits with `{"wait":"director","step":"<validate-or-deliver>"}`: a front that rebased three times in one stay, or a copy that cannot rebase, such as one whose forge gives no base tip.
Only `CLEAN`, `UNSTABLE` and `HAS_HOOKS` confirm a PR clear as it stands. GitHub recomputes `mergeStateStatus` after the base moves, reporting `UNKNOWN` or `BLOCKED` meanwhile, so every other status of an open PR (`UNKNOWN`, `BLOCKED`, `DRAFT` or any unrecognised one) is measured locally: it is behind when its head lacks commits of the base tip, with the same evidence as a GitHub `BEHIND`, and clear otherwise; a local measurement is never conflicting.
So when the task ahead merges, the new front is rebased onto the moved base before it tries to merge, rather than being refused as not up to date.
Observations must match the published head; a moved PR or a failed observation is an error, not permission to merge.
For any PR but a confirmed one, the forge reads the base tip, the base branch's commit on the remote when measured, with `git ls-remote <remote> refs/heads/<baseRefName>` rather than trusting GitHub's possibly stale recorded base; a missing or ambiguous tip is an error.
It fetches that exact tip without updating a branch and computes the behind count with `rev-list` and conflicting paths with `merge-tree`; it never checks out, merges or rebases the task's worktree.

When the PR waits for the Director, the observation is recorded as document `pr-blocked-<full-head>-<state>` before sending the Director a message with the same suffix and type `pr-blocked`, or, for a front that rebased three times, `rebase-stuck`.
It includes the PR URL, full head commit and base tip, state, paths, behind count and the available commands and options.
Validate and deliver share that identity: a replay, a moved base tip or reaching deliver does not notify again for the same head and state.
The first observation remains immutable; a different head or state gets a new report.
If the observation file was written but its document record failed, the next blocked observation with that identity records the existing bytes rather than regenerating evidence against a moved base.
An advance replays recorded observation messages before walking the workflow, so a notification lost after its document committed is recovered even if the forge has since cleared.
A replayed message whose id the Director already holds with other text, such as a report an older binary sent before the template changed, counts as already reported: the earlier message stands, the new text is not delivered and the advance goes on. This holds for every Director message the engine sends; any other send error still fails the advance.
Replay recognizes only the full-head identity with state `conflicting` or `behind`, not ordinary workflow documents whose names also begin with `pr-blocked-`.
Validate's refresh cache retains the Director wait between reads, and the checks watcher no longer polls a task waiting on the Director.
A Director-initiated advance rechecks when due; deliver rechecks on each advance.
For a PR that waits for the Director, Guildhall prescribes no recovery.
The Director can [send a succeeded step back](step-send-back.md) with its instructions, release a stuck front with [`task hold-merge`](tasks.md#holding-a-merge), hold the task, leave the work unadvanced, act externally and recheck, or, at the Guildmaster's request, [cancel the task](tasks.md#task-cancel).
Existing attempt stop and Director-stage abandonment paths are unchanged.

## The merge queue

Each repository has one merge queue of authorized merges, kept in the store as entries plus a front record, buckets `merge-queue` and `merge-holds` created on first use ([decision](decisions/merge-queue-rebase.md)).
A merge is authorized once its merge decision is answered `merge`, or once deliver recorded `<deliver>-<k>-authorized`, its own or a [carried-over](#approval-carry-over) one.
Deliver then joins the queue, `JoinMergeQueue`, under that decision or authorization id.

- **Ready time.** A task's ready time is the commit time of the receipt that first authorized its merge: the answer of the merge decision, or the recording of the `-authorized` document; it is never wall-clock time.
  A [major send-back](#tiered-rebase) takes the task out of the queue but keeps its entry and ready time, so the rebuilt head, once authorized again, rejoins at its first place.
  `task hold-merge` keeps it too; only a merge or a cancellation removes the entry.
- **Order.** The queue is ordered by [priority](tasks.md#priority), `p0` first, then ready time, then task id (`store.OrderMergeQueue`).
- **Front.** The first task in queue order that is not held claims the front with `ClaimMergeFront`, in one store mutation, so two tasks advancing at once never both hold it; any other claim is refused with `store.ErrMergeQueued` and records nothing.
  A claim counts the task's stays at the front.
  A claim's mutation id is keyed on the slice's deliver step (`merge-front:<repo>:<task>:<stay>` for slice 1, `merge-front:<repo>:<task>:<deliver>--s<k>:<stay>` for a later slice, and the same for its `merge-release`), and the engine acts as the front only when the store's front record names the task, so a replayed claim receipt never makes a front.
  The front stands while its task is queued: until it merges, is sent back or is cancelled, or, once it is held, until its next advance releases it; no other task can claim the front before then, so a held front still pushing or merging never shares it.
  A later priority change reorders only the tasks behind it; nothing is preempted.
- **Behind the front.** A task that is not the front records nothing more and returns `{"wait":"merge-queue","step":"<deliver>"}` (`WaitMergeQueue`), with one `merge-queued` message per place and front, naming its place and the task at the front.
- **Held.** A task whose merge is held with [`task hold-merge`](tasks.md#holding-a-merge) waits the same way, with a `merge-queued` message saying it is held; a held front releases the front, `ReleaseMergeFront`, at its next advance, before any rebase push, which the [merge-queue watcher](director.md#merge-queue) runs within one `checks_refresh` whatever the task last waited on.
- **At the front.** Deliver reads the PR's [mergeability](#mergeability-observations).
  A clear PR merges as in [publish and deliver](#publish-and-deliver); a behind or conflicting one is [rebased](#tiered-rebase), then rechecked, and merges once clear.
  A merge, `LeaveMergeQueue`, removes the entry and releases the front.

Nothing advances a queued task from inside another task's advance.
The session supervisor's [merge-queue watcher](director.md#merge-queue) advances tasks waiting on `merge-queue`, and each held front, only while the Director session is `recovering` or `running`, so a paused Director pauses the queue; with no supervisor, `task start` advances one.
`director status` lists every queue as `merge_queues`, and `task list --json` each queued task's entry as `merge_queue` ([status](director.md#status)).
Records that do not implement `MergeQueue` make every task its own queue's front, in stay 1.

A dependent task's [dependency gate](#the-dependency-gate) reads a queued dependency as `queued for merge` and a held one as `merge held`.

## Tiered rebase

A behind or conflicting PR at the front of the queue, or a conflicting one at [validate](#validate), is brought up to date by Guildhall, in one of three tiers chosen by measured conflicts with no Director turn ([decision](decisions/merge-queue-rebase.md)).
It is the only time Guildhall rebases a PR: one that is only behind at validate, or that is further back in the queue, is not rebased.

**Replay.** A rebase replays the task's whole change as one commit onto the base tip that mergeability measured, `Mergeability.Base`, with [`Workspaces.Rebase`](contracts/git.md#rebase): `git merge-tree --write-tree`, never `git rebase`, so there is one conflict round and the measurement matches the operation.
It is refused while any attempt of the task runs, and so are the squash of a resolution and every move of the task's worktree, `Workspaces.Adopt`: each reads the task's attempts afresh, so an attempt started during the advance still refuses it.
Each rebase is recorded once it is classified as the immutable document `<step>-<k>-rebase-<n>` (`tasks/<repo>/<task>/<step>-<k>/rebase-<n>.json`), schema `guildhall.rebase/1`: the authorization and front stay it was made under (at deliver), from, onto, merge base, kind, reason, the marked head, each conflicting path with its lines and whether it has markers, the total lines, both patch-ids and the rebased head once known.
`<step>` is the deliver step at the front, or the validate step for a conflicting PR there; `<k>` is the step's round and `<n>` counts its rebases.
A later rebase of a rebased head is the next link of a chain that runs from the reviewed head to the head merged.

**Classification.**

| Kind | When |
|---|---|
| `no_conflicts` | No conflicts, and `git patch-id --stable` of `merge-base..reviewed head` equals that of `base..rebased head`: the single-patch form of `range-diff` showing `=`, context lines included and line numbers not. |
| `minor_conflicts` | At most `minor_conflict_files` files (default 3) and `minor_conflict_lines` conflicting lines (default 50), every one with conflict markers; or no conflicts but unequal patch-ids, reviewed as a whole diff. |
| `major_conflicts` | Anything else, including a conflict without markers (binary, delete/modify, rename). |

Conflicting lines are the lines between `<<<<<<<` and `>>>>>>>` on both sides, without the marker lines; the thresholds are the repository's [`[repository.merge]`](configuration.md#rebase-settings) settings.
`git.Program.Classify` and the [rebase-aware review](#rebase-aware-review) of a new build head are not used and do not change.

**`no_conflicts`.** Guildhall moves the task branch to the rebased head, `Workspaces.Adopt` (`git reset --keep`, which never touches untracked stage outputs), and force-pushes it to the same PR with `Forge.Publish`, recording `<step>-<k>-rebase-<n>-pr` and sending `rebased`.
It then waits on `{"wait":"checks"}` for the rebased head's checks, read and paced as validate reads them, and records the finished read as `<step>-<k>-checks-<head>`.
It checks mergeability again and, at deliver, [authorizes](#approval-carry-over) and merges the rebased head.

**`minor_conflicts`.** Guildhall commits the conflict-marked tree as an unpushed head, `marked`.

- It starts an attempt of the synthetic step `<step>-rebase`, run by the copy's profile named `rebaser`, whose input is the conflicting files with markers, the commits on both sides that touch them, and the whole accepted spec.
  Its outputs are a code head with the conflicts resolved and `rebase.json`, `{"behaviour_change": bool, "reason": string}`.
- Guildhall squashes the result onto the base, `Workspaces.Squash`, giving `resolved`.
- It starts an attempt of the synthetic step `<step>-rebase-review`, run by the profile of the workflow's review step, with scope `resolution` and input `resolution`, `diff marked..resolved`: exactly the resolved hunks and nothing else.
- Then it pushes, reads checks, rechecks and authorizes as for `no_conflicts`.

Both synthetic steps are Artisan attempts owned by the step that rebases, not workflow steps, so the workflow and its digest stay as they are; they still count against the [attempt caps](director.md#guard-rails).
A copy without a `rebaser` profile escalates a minor rebase to `major_conflicts` and says so; so do a failed rebaser attempt and `behaviour_change: true`.
A clean replay with unequal patch-ids has no conflicts to resolve, so it skips the rebaser and its narrowed review reads the whole diff.

**`major_conflicts`.** Guildhall records `<step>-<k>-escalated-<n>`, schema `guildhall.rebase-escalation/1`, holding the authorization, from, onto, kind, files, reason, whether the authorization may carry over to the rebuilt head (`carry`) and the spec it was given under (`spec_digest`), and sends the build step that produced the head back, as [`step send-back`](step-send-back.md) does, with feedback naming the base, the conflicting files and the reason; the Director is sent `rebase-escalated`.
At deliver the task leaves the queue, keeping its ready time, and releases the front; normal propagation then reruns review, publish and validate, and the task rejoins the queue at its first ready time once the new head is authorized.
At validate the escalation follows the step's `failed` [loop rule](#rounds-and-loops) back to build, with the same feedback.
Red checks on the rebased head, a narrowed review that requests changes, a failed rebaser and a behaviour change escalate the same way; the next head then needs a new merge decision.

**Again and stuck.** When the base moves again while the front waits for checks, someone having merged outside Guildhall, the front rebases the latest head of its chain again, as rebase `n+1`.
After three rebases in one stay at the front, the task keeps the front, records `pr-blocked-<head>-<state>` and waits for the Director with `rebase-stuck`; [`task hold-merge`](tasks.md#holding-a-merge) holds it and its next advance releases the front, and a later claim starts a new stay with three more.

**At validate.** A conflicting PR at validate runs the same tiers, with the base tip from mergeability, publishes the rebased head to the same PR and reads checks on it; the validate result is bound to the rebased head and names the rebase chain, and deliver delivers that head.
There is no approval yet, so no threshold applies and the later merge decision is for the rebased head.
The `pr-blocked` Director wait at validate remains only for a copy that cannot rebase.

### Review and CI of a rebased head

A rebased head h′ is not reviewed again in full ([R1.36](requirements.md)):

- `artisan_approval` holds for h′ only when an approving review outcome is bound to the reviewed head h, the accepted spec and the current standards, a chain of rebase documents runs from h to h′, and every link is either `no_conflicts` with equal patch-ids or approved with no findings by its narrowed review, whose outcome is bound to that link's rebased head.
  Otherwise deliver fails with `ErrReviewGate`, as for any head the review did not cover.
- `ci_green` for h′ comes from `<step>-<k>-checks-<h′>`, the read of h′'s own checks; validate's result stays bound to the head it validated.

### Approval carry-over

At deliver, a rebased head needs a merge authorization of its own.
A decision already bound to it stands, as any merge decision does.
Otherwise the chain's kind, the hardest of its links, is compared with the repository's `reapprove_after_rebase`, in the order `no_conflicts` < `minor_conflicts` < `major_conflicts` < `never`.

- At or above it, where `guildmaster_approval` is required, deliver opens a new merge decision for h′, whose question begins by saying why the authorization does not carry over.
- Below it, deliver records `<step>-<k>-authorized`, schema `guildhall.merge-authorization/2`, which adds to the [authorization document](#publish-and-deliver) `carried_from` (the original decision or authorization id), `rebase` (the rebase documents of the chain), `kind` and `spec_digest`, sends `approval-carried` and merges.
  It does so only when every gate holds for h′, `nothing_open` holds with no new findings, and the accepted spec digest equals the one at the original authorization; otherwise it asks the merge decision.
  An authorization already recorded for h′ stands: a replay after a crash, or a retry after the forge refused the merge, merges under it again and never records another.
- A narrowed review that approves with findings or suggestions fails `nothing_open`, so it opens a new merge decision for h′; a `merge` answer overrides it as usual.
- An unknown rebase kind, or an unknown threshold, always needs a new decision.
- Under `never`, which config parse refuses unless `artisan_approval` is required, a head rebuilt after a major send-back of an authorized head carries over that authorization too, under the same checks: every condition after `guildmaster_approval` holds, `nothing_open` with no findings included even where it is not required, and the accepted spec is the one the authorization was given under.
  The escalation records that spec as `spec_digest`: the `-authorized` document's, or, for a merge decision, the one the approved head's review outcomes were bound to; otherwise the merge decision for the rebuilt head says why it does not carry over.

The threshold matters only where `guildmaster_approval` is required; without it, deliver authorizes h′ by itself whenever the requirements hold.
`task timeline` shows a carried-over merge as merged by the carried-over approval of the original decision after a rebase of its kind.

## Validate

Validate reads its `head` input, which must be a publish step's output: a head no publish step pushed is refused with `ErrUnsupported`, and so is validating with no `Engine.Forge` ([decision](decisions/delivery-stage.md)).
When the step's latest result is for that head and `passed`, the step is complete and nothing is called.
Otherwise each advance first reads the PR's [mergeability](#mergeability-observations).
A behind PR reads its checks as usual; the merge queue rebases it later, at the front.
A conflicting PR runs no `pull_request` checks on GitHub, so it could never pass: validate runs the [tiered rebase](#tiered-rebase) on it instead, recording `<step>-<k>-rebase-<n>`, publishes the rebased head to the same PR and reads that head's checks, and its result is bound to the rebased head and names the rebase chain.
A `major_conflicts` rebase, a behaviour change, red checks on the rebased head or narrowed-review changes take the step's `failed` loop back to build, as a failed result does below, with feedback naming the base, the conflicting files and the reason.
Then each advance calls `Forge.Checks` for the repository's `path`, the remote the head was published to and the head (the rebased head after a rebase), the commit's checks rather than whatever its pull request is at now:

- **Pending.** Some check is pending, none has failed, or no check is reported yet: nothing is recorded and the task waits with `{"wait":"checks","step":"<step>"}`.
  The Director is told once per head as `<repo>.<task>.checks-pending.<step>.<first 12 hex digits of the head>`, type `checks-pending`.
  A repository configured [`no_ci`](configuration.md#repositories) has no checks to wait for: there, no check reported is finished and passes, with the result marked `"no_ci": true` and the `checks-passed` message saying the head has no CI; checks the forge does report still count.
- **Finished.** Every check has passed, or at least one has failed even while others are pending: round `k` (1, or one more than the latest) records `<step>-<k>-result` (`tasks/<repo>/<task>/<step>-<k>/result.json`), the step's output port, a `guildhall.validation-result/1` document holding the remote, pull request number and URL, the head, the verdict `passed` or `failed`, every check with its name, state, URL and the forge's id of that run of the check, sorted as the forge returned them, the names of the failed ones as `failing`, and `no_ci` when set.
  The Director is told as `<repo>.<task>.checks-passed.<step>-<k>` or `.checks-failed.<step>-<k>`, naming the head, the pull request and the failing checks; each advance sends the latest result's message again, so one lost to a crash after its result was recorded is sent by the replay.

**Failed job logs.** A `failed` result also holds `logs`: one entry per failed check, in the order of `checks`, so the builder sees why each failed without asking for the whole log ([decision](decisions/failed-job-logs.md)).
Each entry names its `check`; a GitHub Actions job also has its `job` id and the `command`, `guildhall ci log <repo>/<task> <job>`, that serves its full log ([CI logs](ci-logs.md)).
An entry has either the log's `excerpt` and the bytes `dropped` from the head of the cached log, or `unavailable` with the reason: `not a GitHub Actions job` (another check app, or a commit status), `log fetching is off` ([`ci_log_limit`](configuration.md#settings) `"0"`), `forge does not provide logs` (a forge without `CheckLogReader`), `log limit of 10 per round reached`, or gh's own reason, such as an expired log.
Only a round about to be recorded fetches logs, after the new-evidence test below and before the result is recorded: pending reads, failed reads with no new failed run and passed rounds fetch nothing, and a result without a failed check is the same document as before.
A round fetches at most 10 logs, of its first 10 failed Actions checks in the order of `checks`, each through `Engine.CILogLimit` bytes of its tail.
A log that cannot be had never fails validate; only a cancelled advance is an error.
The **excerpt** is the step of the log that holds its first `##[error]` line, from the last `##[group]Run` line before it up to the next one, or the whole log when it has no error; of that, the last 200 lines without their leading timestamps, trimmed by whole lines from its head to at most 32 KiB.
The log's byte order mark, its CRs and its ANSI escape sequences, such as colour codes, are removed before any line is matched, so the excerpt is plain text.
Each fetched log is kept in the **CI log cache**, `<state>/<repo>/<task>/ci-logs/<job>.log`, beside `<step>.checks.json`, written to a temporary file and renamed into place; a log cut short starts with the line `[guildhall: <N> bytes dropped from the head of this log]`.
The cache is no store document, `task files` does not list it, and it may be deleted at any time: a crash before the result is recorded replays to the same excerpt from the cache, and a pruned cache is fetched again.

<a id="validate-reruns"></a>
**Reruns.** Under the repository's [`flaky_checks`](configuration.md#repositories) policy `rerun` or `rerun-and-file` (the default), on a forge that implements `CheckRerunner`, a failed round about to be recorded first gets one **rerun** of its failed GitHub Actions jobs on the same head, so a flaky check costs no build round ([decision](decisions/flaky-check-reruns.md)).
The policy is read from the live config at each advance; under `off`, or on a forge without `CheckRerunner`, nothing below happens.

- **Eligible.** A round is re-run only when every failing check is an Actions job (it has a job id; a commit status or another app's check never is) with no rerun record for this step, head and check name. Otherwise the round is a real failure now, as before.
  The key is the head and check name, not the run id, so a check that fails again after its rerun is never re-run a second time on that head; a new head gets its own rerun.
- **Wait for the matrix.** GitHub re-runs no job of a workflow run still in progress, so while any other check of the head is pending an eligible round records nothing, sends `checks-pending` rather than `checks-failed`, and waits on `checks`.
- **Rerun.** For each failing check in turn validate fetches the failed job's log into the CI log cache, under the old job id, reads its failing test names from its `--- FAIL: <Name>` lines, records the write-once **rerun record** `<step>-rerun-<first 12 hex digits of the head><8 hex digits of the check name's digest>` (`guildhall.check-rerun/1`: the step, head, check, failed run id, job, URL, test names, why the log could not be read if it could not, and the time), and only then asks the forge to re-run the job.
  It then waits on `checks`, so the [checks watcher](director.md#checks) reads the new run with no Director turn; nothing is recorded as a result, nothing goes through the loop rule, and no round of `max` is spent.
- **Refused.** When the forge refuses or fails the rerun, such as for a run too old to re-run, validate records why as `<record>-outcome` (`guildhall.check-rerun-outcome/1`) and the round is a real failure in the same advance, so CI trouble never stalls a task.
- **Crash.** A crash after the rerun record but before the forge's answer leaves a record whose rerun may not have happened; when a later read still shows that check's failed run with nothing pending, the rerun counts as spent and the round is a real failure. The forge is never asked twice for one check on one head.
- **Fails again.** The new run is a new run id, so the new-evidence test below sees a new failure, and the round is a real failed result that also names, as `reruns`, each check that failed again after its rerun, with its failed run; its `checks-failed` message says `failed again after one rerun`.
- **Passes.** A passed result is recorded only when the forge reports every check of the head passed, as for any round; it also names each check re-run on the head as `reruns`, with its failed and passing run ids, and the policy as `flaky_checks`.
  A check that failed and then passed on its rerun is a **flake**. Under `rerun-and-file` the Director is told of each as `<repo>.<task>.flake.<step>.<first 12 hex digits of the head>.<8 hex digits of the check name's digest>`, type `flake`, before the `checks-passed` message; its text, built from the rerun record and the result alone so a replay sends it again unchanged, starts `policy: rerun-and-file` and names the check, its platform (the parenthesised end of a matrix check's name, such as `macos-latest`), the job, the failed run and its URL, the passing run, the failing test names or `no test name found in the log`, and the `guildhall ci log` command of the old job. Under `rerun` a flake sends nothing more than `checks-passed`.

A rerun never marks a check passed: the gate below and [`ci_green`](configuration.md#merge-requirements) read only results whose every check passed, and the repository's merge requirements always apply.
The checks of a rebased merge-queue front are read without reruns.

A `passed` result completes the step.
A `failed` result is a blocking result of the step's `on.failed` [loop rule](#rounds-and-loops): within `max` the rule's target, which must be an Artisan step, runs again with the result as `feedback`; the new head is then reviewed, published and validated in the next round.
Past `max` the loop exit `<step>-x<round>`, bound to the head and naming the failing checks, offers only `continue` and `reject`: red checks cannot be accepted.
Without a rule, the task waits for the Director (`director`, naming the step).

**Waiting on a failed head.** Three states leave the step waiting on the checks of a head whose latest result failed: no `on.failed` rule (`director`), a loop exit answered `reject` (`director`, naming the decision), and a loop target that ran and left the head unchanged (`checks`).
In each, an advance reads the head's checks again, so a check the Director re-ran counts; Guildhall's own [rerun](#validate-reruns) comes before the failed round, so a head waiting here already had it, and a check that fails again here is re-run only if it was not re-run on this head before.
A read records a new round only when it is new evidence: a pass, or a failed check run, by check name and run id, that is not among the latest result's failed runs, which is a new failure or a re-run that failed again.
Failed runs that are all among the latest result's, the same ones or fewer, such as while one failed check re-runs or after it passes and another stays failed, keep the step waiting and spend no round of `max`, so a build that leaves the head unchanged launches nothing more until a check fails anew or everything passes.
A pass completes the step; a new failure follows the loop rule as any failed round does, so after a rejected exit it opens the next loop exit.
A forge that gives no run ids cannot show a re-run that failed the same way, which then reads as unchanged.

**Pacing.** `Engine.ChecksRefresh`, the config's [`checks_refresh`](configuration.md#settings) (default one minute), paces the reads: once a validate step has read a head's checks, an advance reads that head's checks again only after `ChecksRefresh` has passed, and otherwise returns the same wait without calling the forge.
The time of the last read is kept in `<state>/<repo>/<task>/<step>.checks.json`; a new head is read at once, and zero reads at every advance.
Nothing in the engine reads the checks in the background: an advance does, such as `task start`, or the Director session supervisor's [checks watcher](director.md#checks).

The **validation gate**: when `deliver`'s `validation` input is wired, its document must be a `passed` result for the head being delivered, or `deliver` is refused with `ErrValidationGate`.
When it is not wired and `ci_green` is required, `deliver` is refused with `ErrValidationGate: deliver step "<id>" takes no validate result`, the floor's wording, so a workflow that omits deliver's `validation` input never merges without its checks being read; with `ci_green` off, an unwired input holds and the merge question shows `ci_green: not required`.
A head the front rebased is covered instead by the read of its own checks ([review and CI of a rebased head](#review-and-ci-of-a-rebased-head)).
Because validate only completes on a passed result for its current head, the gate holds whenever the walk reaches `deliver`; it is kept deliberately as defence in depth, as the review gate is, and guards a workflow and records that disagree.

## Rounds and loops

Before recording attempt `n` of an Artisan step, the engine records its binding as document `<step>-<n>-inputs` (`tasks/<repo>/<task>/<step>-<n>.inputs.json`): every input it is given, by port, path and digest, and for a review or document review the revisions its outcome must be bound to (subject, accepted-spec digest for code, `Engine.StandardsRevision`).
When the attempt follows the Director's retry of attempt `n-1`, the binding also records that retry as `retry`: the retried attempt's number and outcome, the Director's reason, its intake rejection and the summary of its result, read with `adapter.ReadResult` and empty when none reads (stopped attempts, launcher failures, malformed results).
Only that latest retry is recorded; first attempts and loop or stale re-runs have none, and older bindings without it still read and verify.
Preparation passes it to `render.Stage.Retry`, so an advance that resumes an attempt recorded but not launched renders it without the event, and a retry repeated after a refusal keeps the bound reason in both the prompt and the `attempt-retried` message.
It is not an input, so it plays no part in staleness or in checking a published preparation.
When `Engine.Overrides` names the task, the binding also records its [task overrides](tasks.md#task-overrides) as the advance applied them: `overrides`, each applied one's `key`, `value` and record `number`, and `skipped_overrides`, each one that did not apply with its `key`, `value` and `reason`; a task with none records neither, so its binding is byte-identical to one made before overrides existed.
They are not inputs either, so setting or unsetting an override never makes a finished attempt stale.
New review preparations pass that binding's exact revisions to `render.Stage.Revisions` and its range base to `render.Stage.ReviewBase`, leaving the latter empty when no range was recorded.
The [review outcome contract](configuration.md#review-outcome) owns revision validity and exact matching, including unconfigured standards.
The base is not resolved again during preparation, including rebase-only and requested-full rounds.
The [rendering contract](rendering.md#artisan-prompts) owns the emitted field, and [attempt preparation](#attempt-preparation) owns published-prompt immutability, including prompts prepared before this field existed.
Besides its wired inputs, an attempt may be given:

- `resolution`: see [rebase-aware review](#rebase-aware-review);
- `previous`: a review step's own latest outcome, whose findings the new outcome must disposition;
- `rulings`: the Director's [rulings](#director-rulings) on findings of a review step's previous outcome, recorded first as document `<step>-<n>-rulings` (`tasks/<repo>/<task>/<step>-<n>.rulings.json`), a JSON array of rulings;
- `feedback`: each blocking result that sent the task back to this step and that no earlier attempt of it was given: an Artisan's outcome document, or the Guildmaster's exact answer recorded as `<decision>-answer` (`tasks/<repo>/<task>/<decision>/answer.md`);
- `context`: the [task context](tasks.md#task-context) the Director added, when the task has any: with `k` entries recorded as the attempt is bound, document `context-<k>` (`tasks/<repo>/<task>/context-<k>.md`), written once when absent and shared by every attempt bound while the task has `k` entries, holding every entry in number order, each under `## Context <n>` with its time and author and its text fenced. A workflow may also wire step outputs to a port named `context`, as the shipped plan step does; those stay wired inputs.
- `linked`: the documents of other tasks the Director [linked](tasks.md#task-links) to the task, when any link is active as the attempt is bound: first, with `m` link records, the index document `links-<m>` (`tasks/<repo>/<task>/links-<m>.md`), written once when absent, listing each active link's number, source, path among the inputs, digest, time, author and the Guildmaster's words fenced; then each linked document as recorded, in link order, pinned by the digest its link recorded. Every linked file is checked against that digest before the binding is recorded, and a missing or changed one fails the advance with an error naming the task, the link and its source, binding nothing. The design round's designer is given `context` and `linked` too.
- For a [Director stage](#director-stages) a loop sent back, besides that `feedback`: `previous`, the stage's own latest document on each output port, and `reviewed`, each document a blocking result among the feedback was bound to (a Guildmaster decision's or a document review outcome's subject), once each; a result bound to a head has none.
  The grill resumes from them rather than starting over (`baseline/stages/grill.md`).
  A [design round](#design-rounds)'s `changes_requested` answer is such feedback for the grill too, with the design as `reviewed`.
- `design`: for a `plan` step, the design of the task's latest [design round](#design-rounds) the Guildmaster approved, `<decision>-<n>-design`, when there is one.

`previous`, `rulings`, `reviewed`, `resolution`, `feedback`, `linked` and the engine's `context` document play no part in staleness: only the wired inputs do, and plan's `design`, so a newly approved design makes a finished plan stale.

Feedback a failed or stopped attempt was given is still pending, so the attempt that retries it is given it again.
A succeeded attempt stays current while its wired inputs, and for a review its revisions, equal what the step would be given now and no feedback is pending; otherwise the next attempt starts.
The engine-added inputs play no part in this, so new rulings, new task context or a new or removed task link never make a finished attempt stale; the engine's `context-<k>` document is told from a wired `context` input by its path.
An attempt's binding is recorded once, so task context or a task link added after it, while it runs or before a recorded attempt is launched, never reaches it.
So when a looped build lands a new head, every review after it runs again on that head.

A result is **blocking** when it is a `changes_requested` outcome, whatever its findings cite, or a Guildmaster's `changes_requested` answer ([decision](decisions/review-outcome-2.md)).
Comments without a rule are suggestions, listed in `suggestions` in /2: a reviewer with only those records `approved`, which completes the step (`docs/review.md`); a /1 outcome requesting changes with only rule-less findings still blocks.
A `validate` step's `failed` result is blocking too, and is one round of its `on.failed` rule ([validate](#validate)).
Each blocking result of a step is one round of its `on.changes_requested` rule (`on.failed` for validate); the step may loop `max` rounds, plus one for every loop exit answered `continue`.

- **Within `max`.** The rule's target runs again with the result as `feedback`.
  The target of a `changes_requested` rule may be an Artisan step or a Director stage, which starts its next attempt as a new `director-stage` hand-off; a `failed` rule's must be an Artisan step.
  When the target has run and the step's inputs did not change, as when a build returns the same head, the step runs again on the same subject with its previous outcome.
- **Past `max`.** The engine opens decision `<step>-x<round>`, kind `loop-exit`, bound to the round's subject, with options `continue` (one more round), `accept` and `reject`.
  `accept` on a review step records the Guildmaster's approval as `<step>-x<round>-outcome`, an outcome by reviewer `human` bound to the originating review's recorded revisions, and completes the step.
  It retains that review's findings with `unresolved` dispositions and its suggestions unchanged, with the summary "Accepted by the Guildmaster at loop exit in decision `<id>`; `<n>` finding(s) stay unresolved.", so acceptance does not erase findings or claim they were fixed, and later rounds must disposition them.
  The revisions come from the originating human decision's binding or Artisan outcome, never from the configuration at answer-processing time, so stale acceptance cannot satisfy the delivery gate.
  On a `guildmaster-approval` step, `accept` records `<step>-x<round>-spec`, checked against the exit decision's document subject by the spec gate.
  Output resolution orders an exit output immediately after its originating round, before later rounds, for all downstream consumers.
  `reject` leaves the task waiting for the Director, told as `<repo>.<task>.loop-exit-rejected.<id>`.
- **No rule.** The task waits for the Director, told as `<repo>.<task>.changes-requested.<step>-<n>` (or `.<decision>`).

When an attempt's review outcome arrives, it is refused with `ErrOutcome`, recording nothing, if it is not bound to the revisions in its binding (a stale outcome) or if it gives no disposition for a finding id of the previous outcome it was given.
Suggestions need no disposition; when the previous outcome is a /1 document, only its rule-cited findings are findings, so a /2 reviewer dispositions only those.

### Director rulings

A review step's attempt is given the [rulings](tasks.md#director-rulings) captured in its recorded `rulings` input on its previous outcome.
Review starts resume an existing immutable binding before creating extras; launch and contest processing both consume that binding, so later rulings cannot enter a replayed attempt.
When its outcome keeps a ruled finding open, `unresolved` or `disputed`, the attempt contests that ruling: the engine records it once, before the attempt is finished, naming the attempt.
A contested ruling on a rule-cited finding of a blocking outcome is a dispute: the engine opens that round's loop exit `<step>-x<round>`, even within `max`, and neither the loop's target nor the step runs until it is answered.
The question identifies the head or document and includes compact finding/rule/move details and references to persisted outcomes and ruling records for complete evidence.
Questions are bounded to the store text limit; long fields and, for very large questions, remaining details are abbreviated.
`continue` upholds the finding and the loop goes on, with one more round allowed; `accept` and `reject` are as at any loop exit.
A finding the attempt resolves leaves its ruling uncontested.
A ruling never resolves a finding, with one exception: a `disputed` finding of an approving outcome with a standing ruling no longer counts for [`nothing_open`](#publish-and-deliver).

### Stall signals

When a review round within its allowed loop count (`max` plus answered `continue` exits) requests changes again, the engine compares its outcome with the previous one and sends the Director message `<repo>.<task>.stall.<step>-<n>`, type `stall`, naming every signal found: the head (or document) is unchanged, no finding was resolved, or a finding is still open, `unresolved` or `disputed`, for two rounds (report §5.3).
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

### The head check

With `Engine.Workspaces`, launching an attempt of a step whose single output port is a code head, a build including its fix rounds and send-back attempts, first records document `<step>-<n>-base`, at `tasks/<repo>/<task>/<step>-<n>/base`: the full commit id `Workspaces.Base` answers for branch `guildhall/<task>` after `Ensure`, the merge base of the task branch and the default branch the workspace last fetched, followed by a newline.
It is recorded once: an attempt recorded but never launched and launched again keeps its first base, though `Ensure` has since moved the recorded default branch.
The merge base, not the branch tip, is the base so that a send-back attempt that rebases the task branch onto a newer default branch still descends from it; on a first build the two are the same commit.
Failing to read or record the base fails the launch, which the next advance retries.

At intake, on both the omitted and the explicit path and before anything is recorded, the head is accepted only if it is the tip of `refs/heads/guildhall/<task>` (`Workspaces.Tip`) and the recorded base is it or one of its ancestors (`Workspaces.IsAncestor`).
Otherwise the result is [rejected](#replay-and-recovery) with a reason such as `head abc1234 is not the tip of guildhall/t7 (tip def5678)` or `head abc1234 does not descend from the attempt's base 0a1b2c3`: a head on another branch, a detached HEAD that is not the tip, a head the repository lacks and a branch reset onto unrelated history are all refused, so publish never pushes them.
Work that resets the branch onto the default branch and commits again still passes.
The check is skipped when the head document is already recorded, so a replay after the branch moved on keeps the accepted head; with no `Engine.Workspaces`; and for an attempt with no recorded base, one launched before bases were recorded.
A git failure, a missing branch and an ended context reading the tip or ancestry are operational errors that leave intake replayable, never a rejection.
The task's custody lock serialises advances; external writers must not change the worktree during intake.

## Report-only workflows

A workflow with no `deliver` step, such as the shipped `hallmark` workflow of a [hallmark task](tasks.md#task-kinds), finishes once every step is done ([decision](decisions/report-only-workflows.md)).
The advance records document `finished`, at `tasks/<repo>/<task>/finished.json`, `{"schema":"guildhall.finished/1","head":…}`, and sends the Director a `finished` message, id `<repo>.<task>.finished`, naming the last step's output.
It sends no `merged` message and opens no audit, so a hallmark counts toward no hallmark cadence.
When the last step is a `hallmark` step that recorded a report, it opens decision `follow-ups`, kind `follow-ups`, and the task stays open, waiting on it: the order is `finished` recorded, the decision opened and announced (`<repo>.<task>.decision.follow-ups`), and the `finished` message, which says the decision is open, that the task stays open until it is answered, and that `guildhall task start --decision follow-ups --repo <repo> <task>` then closes it; the advance returns `{"wait":"decision","step":"<last>","decision":"follow-ups"}`.
A deferred decision is still open, so the task keeps waiting.
The decision is bound to the latest attempt's report as a document subject and offers `pick` and `none`; its question names the repository, task and report path, lists the report's `### H<n>. <title>` headings in order, best effort and cut with `… see the report` to fit `MaxTextBytes`, and asks for the picked ids on the lines after `pick`.
Answering it records the answer only, as for any decision; the Director creates the picked tasks.
The advance after the answer, `task start --decision follow-ups`, finishes again: `finished` is already recorded and the decision and `finished` messages are resends, so nothing is sent twice, and it [closes the task](#publish-and-deliver), voiding any decision still open, sets it `done` and returns `{"wait":"done"}`.
A report-only task with no `follow-ups` decision, one whose last step is not a `hallmark` step or whose report cannot be read back, closes at once on the advance that finishes it.
`head` is set only when every step that runs attempts is a `hallmark` step: the prepared commit of the last step's latest attempt, at which [prune](running.md#pruning) retires the worktree as it does at a merge record's head; without it the worktree is kept.
A definition that has a deliver step and somehow runs past it is still refused with `ErrUnsupported`.

With `Engine.Workspaces`, launching an attempt of a `hallmark` step records document `<step>-<n>-prepared`, the worktree's HEAD after the task branch was fast-forwarded, once, as the head check records its base.
At intake, before its report is recorded, the result is [rejected](#replay-and-recovery) when the worktree has a staged or unstaged tracked change, or HEAD is not the prepared commit, with a reason starting "hallmarks must not change the project"; untracked files are allowed, and a report already recorded stands on replay.
A git failure is an operational error, never a rejection.

The first attempt of a `hallmark` step that wires no `previous` input is given, as `previous`, the report of the newest done task of the same repository, by close time then id, whose bound workflow has a `hallmark` step and that recorded a `<step>-<n>-report`.
The report is copied once into the task's documents as `previous-<step>`, at `tasks/<repo>/<task>/previous-<step>-<source task>.md`; later attempts reuse the copy and every binding records it, so a hallmark that finishes later never changes what an earlier attempt was given.
With no such task the input is absent.

Every attempt of a `hallmark` step that wires no `baseline` input is given, as `baseline`, the evidence a hallmark cannot gather under a shipped grant, rendered once for the task as document `baseline-<step>`, at `tasks/<repo>/<task>/baseline-<step>.md` ([decision](decisions/report-only-workflows.md)).
Whichever attempt is bound first renders it; later attempts reuse it, and a replayed advance reads no forge or git once it is recorded.
It names the previous hallmark, chosen as `previous` chooses it, at the head of its `finished` document, or else its newest prepared record; the current head, the remote's default branch as `Ensure` fetched it when the attempt was bound (the prepared record, written later at launch, stays the authority, and a later attempt may run on a newer head); and every merge whose commit (its merge commit, or else its pull request's head) is in the current head and not in the previous one.
A merge is any `<id>-merge` document of any other task in the repository, open, done or cancelled, each record read on its own, so a task that is still open after its merge and every merged slice of a multi-PR task are listed; two records of one task at the same commit are one row, the higher slice's or else the later name's.
A slice of a multi-PR task is labelled `<task> · slice <k>/<n>`, with `n` from the task's accepted spec (`<task> · slice <k>` when that cannot be read), in its row, its checks heading and its failed jobs; a one-slice task's label is the task id.
Rows are newest first by landing, a commit that is an ancestor of another's sorting after it (`Workspaces.IsAncestor`), at most 20 after that sort, and the baseline says how many more it dropped and names on a `Not placed` line any whose ancestry could not be read.
A merge record that cannot be read, does not decode, is not a pull-request document or names no valid head is not dropped: it is a row `| <label> | unreadable: <reason> | — | — |` after the placed rows, outside the limit and with no checks, and is also on the `Not placed` line as `<task>: merge record unreadable: <reason>`.
With `Engine.Forge` it lists each merge's checks from `Forge.Checks` on its merge commit, name, state and URL, a failed GitHub Actions job's row naming the [`guildhall ci log`](ci-logs.md) command that serves its log, and ends with a fenced `guildhall.hallmark-baseline-jobs/1` JSON block of those jobs.
A forge error is recorded under its merge as "checks unavailable: <reason>" and the attempt still launches; only the advance's context ending is returned, so a cancelled advance records no baseline.
Without `Engine.Workspaces` the current head is unknown and the merges unavailable, and without a forge the checks and the block are absent.
Like the other engine-added inputs, the baseline never makes a finished attempt stale.

## Review gate

Before `deliver`, every `review` and `document-review` step earlier in the workflow must have a latest recorded outcome, `<step>-<n>-outcome` or a loop exit's `<step>-x<round>-outcome`, that is neither blocking nor `rejected` and is bound (`ReviewOutcome.BoundTo`) to its current document subject or the head being delivered, the step's applicable accepted-spec digest and the current `Engine.StandardsRevision`; otherwise `deliver` is refused with `ErrReviewGate`.
`publish` runs the same gate over the review steps before it, bound to the head it would publish, before it pushes anything.
Routing never walks a review that is not approved on to publish, so this guard only fires on a routing regression; it is kept as defence in depth.
A failing guard is recorded as the round's refusal, `publish-<k>-refused`, then `publish-<k>-refused-2` and on (`tasks/<repo>/<task>/publish-<k>/refused-<digest>.json`), holding schema `guildhall.publish-refusal/1`, the head, whether `artisan_approval` was required and the gate's reason; a refusal whose content is already recorded is not recorded again.
When the repository's `artisan_approval` is required, nothing is pushed and the advance fails with `ErrReviewGate`, which reaches the Director as the advancing command's error or the watcher's [`advance-failed`](running.md#the-watcher) message; when it is not, the refusal records the reason, as at deliver, and publish goes on.
So several review stages, such as the Guildhall repository's three reviews on distinct profiles, must all approve the same head.
A head Guildhall rebased from the approved head is covered by its recorded rebase instead ([review and CI of a rebased head](#review-and-ci-of-a-rebased-head)).
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
This is not the narrowed review of a [tiered rebase](#tiered-rebase), which Guildhall starts itself on a head it rebased, with scope `resolution`, and which reads only the resolved hunks.

## Documents and inputs

A succeeded result's listed output files are copied from the task's working directory into the Director document directory at `tasks/<repo>/<task>/<step>-<n>/<output path>` and recorded with their sha256.
An omitted code-head output follows [code-head output intake](#code-head-output-intake) instead.
Output reads are rooted in the working directory: file and parent-directory symlinks cannot escape it.
All outputs, including supplemental ones, are read and checked before any are copied or recorded.
The first output is the step's output port, named `<step>-<n>-<port>`; later outputs add `-<index>`, from 2.
A code-head output must hold a 40 or 64 lowercase hex object id and pass [the head check](#the-head-check), and a review outcome must parse, or nothing is recorded.
Every review outcome, including human review and loop-exit acceptance, projects its summary, findings, suggestions and dispositions in its document reference for inspection and rulings.
A human approval's summary is "Approved by the Guildmaster in decision `<id>`.".
An input `step.port` resolves to that port's document of the latest round, and its file is checked against the recorded digest before it is used.

## The working directory

Every Artisan attempt of a task runs in one working directory, which a Director stage's outputs are also copied into, `<state>/<repo>/<task>/work`, a git worktree of the repository's configured `path` ([decision](decisions/worktree-custody.md)).
Before each launch the engine calls `Workspaces.Ensure`, whose contract is [`internal/git`](contracts/git.md#workspaces), with that path, the repository's `path` and `remote`, and branch `guildhall/<task>`, the branch publish pushes to:

- the first attempt creates the worktree on a new local branch `guildhall/<task>` at the commit the remote's default branch is at then, or on an existing local branch of that name as it is;
- every later attempt, including a retry, a loop round and a relaunch, reuses it: Guildhall never resets, cleans or checks out over it, so an Artisan uses git freely inside it and its commits, branches and uncommitted files carry to the next attempt.
  It only fast-forwards an untouched branch, below, and a [tiered rebase](#tiered-rebase) moves the branch to the rebased head with `Workspaces.Adopt`, `git reset --keep`, only while no attempt runs, keeping untracked and local files and refusing rather than overwrite one;
- listed output files sit beside the code; an Artisan that commits should leave them out.
  An omitted code-head output is recorded outside the worktree ([code-head output intake](#code-head-output-intake)).

Ensure is replay safe: the worktree is built beside the working directory and moved into place only when complete, so a crash leaves no working directory or a complete one, and the attempt, recorded but not marked launched, is launched by the next advance.
When the working directory cannot be made, such as when the remote is unreachable or the directory holds files but is not a worktree of the repository, the advance fails before launching anything and running it again retries; a plain working directory an earlier Guildhall made for a task that is still open is refused this way rather than replaced.
Every Artisan attempt of a workflow step, whether a first attempt, a retry, a loop round, a send-back or a relaunch, then calls `Workspaces.FastForward` with the same workspace, before the attempt's [base](#the-head-check) is recorded ([decision](decisions/branch-kept.md)).
An untouched branch, one with no commits that are not on the default branch, moves to the commit the remote's default branch is at now, with `git merge --ff-only`, keeping untracked outputs such as the grill's, and `DefaultRef` is refreshed, so a build retry after main moved starts on the new main and records it as its base.
A task held by the [dependency gate](#the-dependency-gate) already has its worktree, made for its grill at the main of that time, and starts research on the main its dependencies merged into this way.
A relaunch repeats the fast-forward, which is harmless because it is idempotent.
A Director stage and the tiered rebase's synthetic `<step>-rebase` and `<step>-rebase-review` attempts are never fast-forwarded, and nothing is without `Engine.Workspaces`.

This only fast-forwards, and a branch with commits is rebased only later by the tiered rebase.
`FastForward`'s kept reason is free text, so the engine sorts it by asking `Workspaces.IsAncestor` whether the branch tip is an ancestor of `DefaultRef`:

- a branch with commits of its own is kept silently, as the normal case it is;
- an untouched branch that was not moved, because the remote could not be fetched, the worktree is on another branch or git refused, for example because an untracked file would be overwritten, is a **branch kept**: the attempt proceeds on the existing branch and the Director is sent `<repo>.<task>.branch-kept.<step>-<n>`, type `branch-kept`, saying `Step <step> attempt <n> of <repo>/<task> runs on its branch <branch>, which was not moved to the current main, because <reason>.`
  A replay with another reason sends nothing new, so each attempt sends at most one.

A code-head attempt, such as a build, its fix rounds and send-backs, then reads `Workspaces.Behind`, the first-parent commits of `DefaultRef` its branch tip lacks.
When the branch is behind or was kept, its prompt gets a "Behind main" section naming the count, why a kept branch was not moved, and that main's changes are taken, when needed, with `git fetch` then `git rebase origin/main`, never `git merge`.
Research, plan and review attempts never get the note.
The note is rendered only with the attempt's first [preparation](#attempt-preparation); a published prompt is never rendered again, so a relaunch keeps it as it was.
A working directory that is not yet a worktree is left alone: `Ensure` creates the branch at the current main anyway.
When a merge closes the task, the engine calls `Workspaces.Retire` with the merged head and the task's recorded documents, before marking the task `done`, so a replay retires it again.
It removes the worktree and deletes the local branch `guildhall/<task>` only when nothing in them would be lost: no changed or staged tracked file, no untracked file other than a copy of any document the store records for the task at that path below its record directory (an `outcome.json` from a review after a rebase matches although a later-sorting review recorded another), and a worktree HEAD and branch tip that are the merged head or behind it.
Otherwise it removes nothing, and the Director is sent `<repo>.<task>.workspace-kept.<merge>.<digest>`, type `workspace-kept`, naming the directory, the branch and what they hold, where `<digest>` is the first 12 hex digits of the text's sha256; the Director decides what happens to them.
A retirement that fails, such as git refusing to remove the worktree, is reported the same way and never fails the merge or keeps the task open.
A merged head the local clone lacks, as after the forge's "Update branch" and a merge outside Guildhall, is fetched from the remote by its commit id before the check; one that cannot be fetched is such a failure, and nothing is removed.
A retirement cut short by an interrupt is not: the advance fails before the task is marked `done`, no `workspace-kept` message is sent, and running the command again replays the merge and retires the working directory again.
With no `Engine.Workspaces` the working directory is a plain directory, which is how the engine's own tests run; publish cannot push from one.

## History

Every read of where a record sits in a task's history goes through one private view in `internal/interp` (`history.go`), built from the task's records at the start of an advance and updated by each write the advance makes, so a read after a write sees it.
It answers a step's latest attempt, its round decisions and next round, its loop exits and how many were answered `continue`, its blocking results in round order, the originating round of each exit, which feedback an attempt was given, the latest document of a step's output port and the range a code review's latest outcome reviewed.
Record names stay the keys and keep their forms (`<step>-<n>`, `<step>-x<k>`, `<step>-<n>-<port>`, `<step>-<n>-<port>-<index>`, `<step>-<n>-inputs`, `<step>-<n>-base`, `<decision>-revisions`, `<decision>-answer`); they are parsed in one place, from the right, so a step id ending in `-<digits>` or `-x<digits>` is never mistaken for a round of a step it extends.
`RecordPlace` is that parser's exported face, for readers outside `interp` such as the task page: pure, it returns a name's `Place` (the step with its `--s<k>` qualifier read off, slice, round, exit, part and index) and reads, first match wins, `send-back-<decision>`; the delivery sequence `<decision>-rebase-<n>` with its follow-ons `-<sub>`, `<decision>-escalated-<n>` and `<decision>-checks-<head>`, each as its decision's place; a document; then a decision whose step is not itself a document name.
Any other name, such as `follow-ups`, `finished`, `q<n>`, `previous-<step>` or `baseline-<step>`, is ok false.
`DecisionID` and `SendBackName` are the exported builders of a decision id and a send-back's name.
Exit `k` of a step originates in its `k`th blocking result; an exit's output orders immediately after that round and before later rounds.
A missing or unreadable record the view needs, such as the outcome an exit originated in, is an error, never a guess that the step approved.
The view is private: it is not a storage interface and has no double; it runs over both the store and `interptest.Records` through the engine tests.

## Spec gate

Every input wired to an accepted-spec output, such as `build`'s and `review`'s `spec` from a `guildmaster-approval` step's `spec`, must come from an approval decision answered `approved` (or a loop exit answered `accept`) whose subject digest equals the accepted spec's digest; otherwise the step is refused with `ErrSpecGate` before anything is recorded.
The gate follows the source's output type, not the input port's: in a workflow with no acceptance step, such as the shipped `bug`, `build` and `review` take `plan.spec`, a document, which passes through ungated ([decision](decisions/task-kinds.md)).
Review outcomes, the rebaser and deliver's merge authorization bind to the digest of the spec the build was given either way.
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
Missing required outputs, invalid output paths, unreadable files named by the result, invalid port content, a code head that fails [the head check](#the-head-check) and stale or undispositioned review outcomes reject intake.
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

A missing `launched.json` does not prove that no worker holds the attempt's files, so an attempt's preparation is published once and never replaced ([decision](decisions/immutable-attempt-preparation.md)).
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
`director_loop_test.go` runs `testdata/workflows/director-loop.json`, whose Guildmaster document review of the research notes loops back to the Director's grill, on both backends: `changes_requested` hands grill attempt 2 to the Director with `feedback`, `previous` and `reviewed` in its binding and prompt, a crash before the mark replays with no second message, the review is asked again on the same notes, and past `max` the `-x` decision is asked; a `validate` loop to a Director stage is still `ErrUnsupported`.
`TestDefaultWorkflowDelivers` runs the shipped default workflow, its grill overridden to an Artisan with no skills, on both backends from task creation through grill, research, plan, acceptance, build, review, publish, a failed validation looping to build, the new head's review, publish and passed validation, to the merge and the closed task.

`internal/interp/engine_test.go` runs the straight workflow, `testdata/workflows/straight.json` (plan, Guildmaster acceptance, build, Artisan review, Guildmaster sign-off, deliver), twice: against the scripted mock with the in-memory records and `messagingtest.Fake`, and against the same mock with a real store file and its mailbox.
It checks each event's effect on the records, the approval bound to the plan document's digest, the spec gate, a crash between recording and launching an attempt, the zero-model guard, a failed attempt, mismatched events and a changed revision.
`prepare_test.go` checks [attempt preparation](#attempt-preparation): a lost acknowledgement or lost `launched.json` with a changed baseline and skill tree replays the same files and spec, a refused live duplicate leaves every byte and path intact, invalid preparation is refused with `ErrPreparation`, and a worker started from the test binary reads its original input after the replay.
`loops_test.go` runs the round trace of report §7.1 with three code reviews on distinct profiles (`testdata/workflows/three-reviews.json`), where the strict review approves with a suggestion listed, against both backends, and checks, mostly against both, a stale outcome refused, a missing disposition refused, stall signals on an unchanged head, `max` opening the loop exit with `continue`, `accept` and `reject`, a Guildmaster's changes looping to the plan, changes without a rule and the review gate.
`review_verdict_test.go`, red first, checks on both backends that a `changes_requested` outcome whose only finding cites no rule sends the build back with the outcome as `feedback` and past `max` opens the loop exit with no publish or deliver record, and that the publish guard, against a sign-off outcome recorded as `changes_requested` behind an approved answer, pushes nothing, records `publish-1-refused` once and fails with `ErrReviewGate` under a required `artisan_approval`, and records the reason and publishes when it is not required.
Human-review regression coverage includes plan → human document review → approval, code-review provenance, and replay after a failed outcome-record write.
Output containment tests cover escaping file symlinks, parent-directory symlinks, and supplemental outputs.
`rulings_test.go` checks [Director rulings](#director-rulings) on both backends: a ruling given to the next review, resolved or contested once by an unresolved or disputed finding, and a contested blocker opening the loop exit within `max`.
See the [interpreter contract tests](contracts/interp.md#tests) for history-view and per-write replay coverage.
`TestRecordsContract` runs `interptest.RunRecordsContract` against the store and the double, including concurrent list readers and record writers.
