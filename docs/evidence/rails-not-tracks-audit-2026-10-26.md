# Director escape-path audit — 2026-10-26

Companion to [rails, not tracks](../decisions/rails-not-tracks.md).
This is a static source audit at commit `58397669a16d508ece7fea620c25f39990389f9b`, not a new runtime reproduction or a claim of live acceptance.
All `file:line` references below refer to that snapshot, so they remain interpretable after implementation changes.
No engine behavior, runtime prompts, workflow definitions or tests change in this documentation slice.

## Scope and ranking

The inventory covers all interpreter wait variants and the refusal/recovery families on the task advance, result intake, review/loop, publish/validate/deliver, launcher, reconciliation and Director CLI paths.
Repeated I/O, configuration, schema, identity and mutation-conflict errors are grouped by their shared recovery boundary rather than repeated for every `return err`.
Ordinary syntax errors that can be corrected and resubmitted are not dead ends; admission and infrastructure refusals are included where they can strand accepted work or hide progress.
Post-delivery quality judging is not a delivery gate; its existing `quality judged`/`quality judge` recovery remains separate from this task-route audit.
No Herdr lifecycle action was run for this audit.

Likelihood is an engineering estimate, not measured incident frequency: **high** means a reported D3 incident or a common continuation state; **medium** means a normal retry, restart or configuration change can reach it; **low** means corruption, unsupported combinations or a rarer process/platform failure.
Entries are ordered approximately by likelihood within each band, not by severity; low-likelihood custody failures must still fail closed.
Each entry names today's behavior, evidence, the facts and tool the Director needs, and a small follow-up slice.
Suggested tools below are capabilities to build, not assertions that the CLI already implements them.
The existing commands cited are shorthand; invocation must retain the selected copy/store/config/state and repository context.

## High likelihood

### A01 — Published head has no checks, including a conflicting pull request

- **Current:** validate treats an empty check list without `no_ci`, or checks still pending without a failure, as `WaitChecks` indefinitely; it sends one pending message per head, but does not inspect pull-request mergeability, closure or divergence while waiting.
- **Evidence:** `internal/interp/validate.go:124-153`; `internal/forge/github.go:228-279` reads commit checks only; `internal/cli/checks_watch.go:23-32` retries waiting tasks.
- **Director needs:** pull-request state and current/base head, conflict/mergeability facts, check names and ages, the last successful read and whether checks are expected, plus a safe route to an Artisan rebase/fix and then renewed review, publication and validation.
- **Small fix:** add a forge observation contract and a bounded no-progress notification that distinguishes absent checks, pending checks and an unmergeable pull request; then connect it to recorded send-back rather than repeatedly saying to advance.
- **Safety:** do not equate no checks with success or automatically set `no_ci` to escape this wait.

### A02 — Changes requested without a loop, or a successful step needs re-running

- **Current:** a blocking review or Guildmaster `changes_requested` with no loop sends a message and waits for the Director; `step retry` accepts only the latest failed/stopped attempt, so it cannot re-run the succeeded build or review to address that feedback.
- **Evidence:** `internal/interp/loops.go:238-272`; `internal/interp/engine.go:350-377,453-480`; CLI verbs at `internal/cli/advance.go:138-180`.
- **Director needs:** the blocking subject, findings and current inputs, plus send-back to any earlier step with instructions and an explicit new attempt even when its previous result succeeded.
- **Small fix:** a recorded send-back/re-run operation that preserves prior outcomes, binds feedback, invalidates downstream evidence and cannot bypass required review or approval.

### A03 — Merge held on an unchanged head

- **Current:** `hold` returns `WaitDirector` with a message; every later advance finds the same answered decision and waits again, and the decision answer cannot be replaced with `merge`.
- **Evidence:** `internal/interp/deliver.go:196-215`; immutable answers at `internal/store/records.go:417-447`.
- **Director needs:** the hold reason, head and decision lineage, plus a way to ask the Guildmaster again after the hold condition clears, without creating a fake code change.
- **Small fix:** open a new linked merge decision for the same subject on an explicit reconsideration request, leaving the old answer intact and requiring a new Guildmaster answer.

### A04 — Review outcome is rejected, or the Guildmaster rejects a step/loop exit

- **Current:** a valid `rejected` review verdict returns `ErrUnsupported`; a Guildmaster step's non-approved/non-changes-requested answer does likewise; a loop exit answered `reject` returns `WaitDirector` forever for that firing, with no general route or task-abandon command.
- **Evidence:** `internal/interp/loops.go:208-217,300-312`; `internal/interp/engine.go:885-890`; task command inventory at `internal/cli/task.go:112-125`.
- **Director needs:** the rejected subject and reasons, prior attempts and available alternatives, plus replan/send-back, hold, preserved abandonment or a new Guildmaster question.
- **Small fix:** classify rejection as an explicit Director attention state rather than unsupported execution, and add one recorded resolution operation at a time; never reinterpret rejection as success.

### A05 — Succeeded build without a head output: repaired case, remaining operational failure

- **Current:** an omitted code-head output is now derived from committed work and replayed even after launcher-recorded success; dirty tracked work, missing Git state or Git failure still abort intake, leaving a succeeded attempt that cannot be retried.
- **Evidence:** `internal/interp/engine.go:251-260,728-742,771-774`; `internal/interp/head.go:15-29`; `internal/git/head.go` implements the clean-head check; `internal/interp/engine.go:365` refuses retry of success.
- **Director needs:** distinguish already-repaired omission from dirty/unborn/unreadable repository errors, show dirty paths and the recorded head if any, and send a repair Artisan without forging a failed result.
- **Small fix:** expose incomplete-intake status and the exact Git evidence, with a safe repair/new-attempt route that preserves the succeeded result and retries intake only after resolving the underlying problem.
- **Existing evidence:** [build-head decision](../decisions/build-head-output.md) and its linked tests; do not file the old omission as still unfixed.

### A06 — Missing/malformed review output: retry works now, recovery guidance is incomplete

- **Current:** missing required output, unreadable listed output, malformed content and stale/disposition-invalid review outcomes now reject intake into a failed attempt, including a launcher-recorded success; the original result remains and `step retry` is available.
- **Evidence:** `internal/interp/engine.go:728-742,775-800`; `internal/interp/rejection.go:18-30`; `internal/interp/loops.go:364-389`; `internal/cli/director_status.go:400-408` reports the rejection with retry.
- **Director needs:** rejected bytes, expected port/revisions and exact mismatch, plus a corrected next attempt with instructions; retry's reason is currently a notification after launch, not a general instruction input to the new attempt.
- **Small fix:** carry explicit retry instructions into the immutable next-attempt binding and prompt, and include the retry operation in the failure message; retain the existing rejection and replay behavior.
- **Boundary:** a missing/unreadable result file itself or a newer result schema is still an operational error, not this retryable output rejection; see A16.

### A07 — Empty standards revision: repaired case, changed human-review binding still blocks

- **Current:** empty `standards_revision` is now valid when unconfigured and must match the binding exactly; invented nonempty values and whitespace-only values do not satisfy that contract, while a human review accepted under an older standards revision cannot satisfy the current delivery gate and the engine does not automatically reopen it solely for changed standards.
- **Evidence:** `internal/workflow/review.go:51-54,190-198,205-229`; `internal/interp/engine.go:863-898`; `internal/interp/loops.go:512-548`.
- **Director needs:** expected and actual subject/spec/standards bindings, plus a fresh review request that actually schedules or reopens the applicable review, not advice to invent a standards revision.
- **Small fix:** expose the binding delta and add explicit review renewal for stale human evidence; keep exact matching and the [unconfigured-revisions decision](../decisions/unconfigured-review-revisions.md).

### A08 — Forge refuses a merge; retry cannot repair its cause

- **Current:** `ErrRefused` persists refusal evidence and notifies the Director; advance retries the same merge, but cannot itself route to an Artisan conflict fix or adopt a moved remote head; other forge errors simply return an error without a merge-refused message.
- **Evidence:** `internal/interp/deliver.go:220-244,294-317`; `internal/forge/github.go:98-143`.
- **Director needs:** refusal category, local/published/remote heads, protection and checks facts, and tools to inspect, repair via an Artisan, republish, revalidate, renew review and ask for approval on a changed head.
- **Small fix:** classify forge refusal and uncertainty in one attention record with legal actions, reusing send-back from A02 rather than an unconditional retry recommendation.

## Medium likelihood

### A09 — Checks watcher errors are log-only; cached wait can disappear or lie

- **Current:** the watcher logs failed advances to `checks.log`, retries next round, and silently skips missing or invalid `next.json`; advance writes `next.json` only on success, so an error can leave stale cached status and no new Director message.
- **Evidence:** `internal/cli/checks_watch.go:59-70,80-114,130-167`; `internal/cli/advance.go:384-393`; `internal/cli/director_status.go:495-507`.
- **Director needs:** the last attempted advance, timestamp/error and whether cached state is stale, plus replay and inspection commands even when no cached next state exists.
- **Small fix:** persist an advance-failed observation for CLI and watcher advances, report invalid/missing cache when inconsistent with records, and deduplicate wakes without hiding changed errors.

### A10 — Launcher continuation can hang or lose its final notification

- **Current:** the launcher's automatic advance tries twice and then sends `advance-failed`, but the child uses `exec.Command`/`Run` without a deadline; failed outbox sends are ignored, and launches without a copy do not configure automatic advancement.
- **Evidence:** `internal/launch/launch.go:354-416`; `internal/launch/process.go:143-147`; `internal/cli/artisan.go:330-381`.
- **Director needs:** the continuation command's identity, start time, last diagnostic and completion state, plus a bounded stop/reconcile/replay path that distinguishes a finished worker from its still-advancing launcher.
- **Small fix:** bound continuation execution and preserve a pending failure notification for replay when sending fails; document explicit manual advancement when no watcher is configured.
- **Safety:** timing out the observer is not proof that its child or any newly launched worker has stopped; test custody and interruption before permitting another advance.

### A11 — Worker wait with absent, stale or misleading heartbeat

- **Current:** `WaitAttempt` can last indefinitely; fresh heartbeat with no output notifies, but missing/stale/wrong-attempt heartbeat returns no stall message, and a chatty worker can remain active without ever producing a result.
- **Evidence:** `internal/interp/engine.go:419-437`; `internal/interp/heartbeat.go:14-37`; `internal/artisan/launcher.go:308-348`.
- **Director needs:** last heartbeat, last output, result presence, owner liveness and elapsed attempt age, plus `artisan status`, safe stop and proven-exit resume/retry alternatives.
- **Small fix:** emit deduplicated stale/missing-heartbeat and prolonged-no-result observations with evidence; do not infer death from silence or impose an automatic success/waiver timeout.

### A12 — Director stage has no heartbeat or per-task hold/abandon route

- **Current:** a handed stage with no result waits silently on subsequent advances; `step finish` can complete or fail it, but no age-based attention signal exists, and `director pause/stop` acts on the Director rather than a single task.
- **Evidence:** `internal/interp/engine.go:420-434,700-716`; `internal/cli/finish.go:46-89`; `internal/cli/director_pause.go:179-218`.
- **Director needs:** purpose, prompt, output contract, age and open questions, plus finish/fail, per-task hold or preserved abandonment and a reminder after session recovery.
- **Small fix:** include every unfinished handed stage in actionable status after restart, then add task-scoped hold/abandon records without finishing unrelated attempts.

### A13 — `artisan resume` is suggested where it must refuse

- **Current:** status offers resume for `stuck` and `unknown` as well as exited attempts; resume correctly refuses a live or unverified owner, so repeating the suggested operation cannot resolve a live launcher with a silent endpoint.
- **Evidence:** `internal/cli/director_status.go:383-387`; `internal/reconcile/reconcile.go:195-251`; `internal/launch/resume.go:93-108`; stop also fails closed at `internal/launch/launch.go:668-720`.
- **Director needs:** owner identity, endpoint response, heartbeat and containment evidence, plus status/reprobe and a supported identity-checked stop when possible, otherwise an explicit infrastructure/Guildmaster escalation.
- **Small fix:** make legal actions depend on classification preconditions instead of offering resume universally; no `--force` that treats unknown ownership as gone.

### A14 — Workflow/configuration changed after a task was accepted

- **Current:** advance loads the current effective workflow, then refuses if its digest differs from the task's pin; it does not retrieve an archived definition by digest, so changing a workflow for new work can strand existing work.
- **Evidence:** `internal/cli/advance.go:285-288,373`; `internal/interp/engine.go:232-243`; `internal/cli/task.go:330-368`.
- **Director needs:** pinned and available revision identities and the source of the mismatch, plus restore/use-bound-revision or explicit Guildmaster-approved migration.
- **Small fix:** persist and resolve accepted definitions by digest before designing migration; never silently use today's definition for yesterday's task.

### A15 — General task route and lifecycle operations are absent

- **Current:** commands support creation, inspection, findings and full-review requests, but not task hold, abandon, route insertion or in-place splitting; a full-review request is consumed by the next review attempt, not itself a scheduling operation.
- **Evidence:** `internal/cli/task.go:112-125,244-260`; `internal/interp/rebase.go:35-47`; `internal/store/records.go:217-236` has a lower-level terminal-state mutation, not a Director task-resolution command.
- **Director needs:** lawful actions scoped to the task, step and attempt, with lineage and accepted-scope boundaries; creating dependent tasks alone is not a recorded split of an existing task.
- **Small fix:** begin with task-scoped hold and preserve/abandon, then add split and built-in-step insertion as separate contracts on the route ledger from A02.

### A16 — Operational intake failures leave success/running records unrecoverable by retry

- **Current:** absent/unreadable result files, newer result schemas, missing bindings or document I/O errors fail advancement rather than reject the worker output; `step done` can refuse before entering the engine, and retry refuses success or a running attempt.
- **Evidence:** `internal/cli/advance.go:219-229`; `internal/interp/engine.go:728-742,745-760,847-860,1109-1118`; `internal/interp/loops.go:367-379`.
- **Director needs:** distinguish unsupported schema from corruption and temporary I/O failure, show what was already persisted, and restore exact evidence or supersede the attempt through a custody-safe operation.
- **Small fix:** one incomplete-intake diagnostic contract with recoverable actions and a preserved-evidence supersession path; never rewrite historical result bytes or fabricate a successful outcome.

### A17 — Immutable prompt or working-copy preparation refuses replay

- **Current:** damaged/mismatched published preparation is never replaced, and workspace ensure refuses an unrelated or legacy plain directory; the recorded attempt can remain unfinished without ever launching, making ordinary retry unavailable.
- **Evidence:** `internal/interp/prepare.go:35-49,76-100,135-174`; `internal/interp/workspace.go:35-47`; `internal/interp/engine.go:438-452`; `internal/git/workspace.go:56-103`.
- **Director needs:** manifest/digest mismatch, path, owner and launch evidence, plus restore exact preparation or retire an unlaunched attempt and create a new one without deleting its workspace.
- **Small fix:** task-scoped unlaunched-attempt reconciliation, with a proof that no owner can still use its files; preserve both old preparation and work.

### A18 — Failed/stopped retry and capacity/recovery holds

- **Current:** a failed/stopped latest attempt has a usable retry; caps send one refusal per task/day, while paused/recovering/stopping states hold launches; retrying does not lift the hold or free capacity, and automatic resumption after capacity returns is not guaranteed.
- **Evidence:** `internal/interp/engine.go:453-462,599-633`; `internal/store/caps.go:142-158`; `internal/cli/advance.go:118-135`; `internal/cli/director.go:609-663`; `internal/cli/director_pause.go:76-95,125-157`.
- **Director needs:** limiting rail, live versus unreconciled attempt counts, reset time and hold owner, plus ready/resume, reconciliation and the correct later retry; managed policy changes remain outside Director authority.
- **Small fix:** enrich the existing refusal with these facts and lawful next actions; preserve zero-token waits and do not bypass caps merely because a task wants progress.

### A19 — A routine Guildmaster wait is legitimate, but cannot be reconsidered generally

- **Current:** unanswered decisions are durable and listed; `decide answer` records the answer, and advancement is separate; a new subject can open a new decision but an unchanged answered subject has no general reconsideration operation.
- **Evidence:** `internal/interp/engine.go:863-898`; `internal/cli/decide.go:30-124`; `internal/store/records.go:417-447`.
- **Director needs:** exact question, subject, options and authority, the command to record the actual Guildmaster answer, and a linked new decision when circumstances change rather than mutating the old one.
- **Small fix:** share A03's reconsideration primitive across decision kinds with appropriate guards; never answer for the Guildmaster or auto-expire an unanswered approval into consent.

## Lower likelihood and safety-critical boundaries

### A20 — Schema-valid workflow reaches unsupported interpreter behavior

- **Current:** unsupported actor/kind execution, a loop back to a non-Artisan, the unsupported automatic-merge policy, or reaching the end of a workflow with no waiting/delivery step fails at runtime; shape validation is not a complete execution-capability check.
- **Evidence:** `internal/interp/engine.go:279,384-397`; `internal/interp/loops.go:258-264`; `internal/interp/validate.go:285-289`; `internal/interp/deliver.go:172-174`; `internal/workflow/validate.go:113-131,228-245`.
- **Director needs:** the unsupported capability and supported alternatives before task creation, and preservation/escalation for already accepted work.
- **Small fix:** share interpreter capability validation with admission and print supported alternatives; add support in separate slices rather than relaxing invariants.

### A21 — Spec/review/validation gates reject stale or missing evidence

- **Current:** gates correctly refuse missing/mismatched accepted spec, review or passed validation, but lack a general tool to schedule the missing evidence; a workflow admitted below the structural floor may omit checks the gate iterates over, which is an enforcement gap rather than a wedge.
- **Evidence:** `internal/interp/engine.go:1021-1039,1085-1093`; `internal/interp/loops.go:512-548`; `internal/interp/validate.go:301-321`; `internal/workflow/authority.go:242-250`.
- **Director needs:** expected versus observed bindings, all unsatisfied invariants and the responsible step, plus lawful reaccept/review/revalidate routes.
- **Small fix:** enforce the final-delivery invariants independently of workflow shape, then expose evidence deficits to the route planner; approval of a workflow must not waive final review.

### A22 — Launch/startup or stop-ladder failure leaves ownership unsettled

- **Current:** startup waits for endpoint/ownership within a bound and returns a log path; a launcher with survivors after its stop ladder leaves them owned, while failure to persist its finish prevents automatic continuation; repeated launch must refuse a live or unverified prior owner.
- **Evidence:** `internal/launch/process.go:187-201,215-254,270-319`; `internal/launch/launch.go:285-353`; `internal/artisan/launcher.go:451-506`; `internal/launch/launch.go:547-565`.
- **Director needs:** startup phase, owner and survivor identities, original error and any cleanup error, plus inspection and proven-owner-gone resume/sweep, otherwise escalation.
- **Small fix:** persist the startup/cleanup failure bundle and expose the supported next operation; include currently ignored cleanup errors instead of implying the startup failure ended everything.

### A23 — Recovery cannot prove owner gone or containment empty

- **Current:** reconcile returns `unknown` for unreadable outcome/result/heartbeat or ambiguous identity, and resume refuses missing containment, live/unverified owners and units that cannot be emptied within its bound; these are necessary safety refusals with no guaranteed autonomous exit.
- **Evidence:** `internal/reconcile/reconcile.go:160-192,195-251`; `internal/launch/resume.go:93-119,140-171`.
- **Director needs:** each failed proof and surviving identity, platform limit and last probe time, plus safe re-probe, preserve/hold and a maintenance ticket/Guildmaster escalation.
- **Small fix:** an explicit recovery-blocked record and diagnostic export that survives restart; an unsupported platform proof must remain unsupported, never be guessed from PID absence alone.

### A24 — Director recovery, stop or reload cannot finish

- **Current:** recovering requires `director ready`; incomplete stop keeps admission held and asks for another stop; startup refuses a live/unknown supervisor or surviving runtime, and the session supervisor silently keeps running when reading state fails; graceful reload can wait for an open Director stage.
- **Evidence:** `internal/cli/director.go:340-395,609-663`; `internal/cli/director_pause.go:179-218`; `internal/cli/director_supervise.go:443-513`; `internal/cli/director_handoff.go:97-129`.
- **Director needs:** admission reason, stop/reload phase, unreadable state and surviving identities, plus status, finish the handed stage, handoff/reload, ready or repeated safe stop as appropriate.
- **Small fix:** persistent session-control failure/age diagnostics and precise next actions; do not turn a missing state read into endless silent `keepRunning`.

### A25 — Missing notification or unavailable wake

- **Current:** inbox persistence precedes a bounded wake, so no session/pane or a paused/stopped runtime queues rather than loses a normal message; however not every error path sends a message, and some launcher sends discard errors entirely.
- **Evidence:** `internal/cli/advance.go:314-319`; `internal/messaging/wake.go`; `internal/launch/launch.go:385-388`; `internal/cli/checks_watch.go:130-167`.
- **Director needs:** receipt versus notification versus wake status, pending incidents and send failures, plus list/read/ack and later replay, not a claim that typing into a pane delivered authority.
- **Small fix:** durable pending failure notifications with bounded retry on later commands and visible stderr/local evidence when the store is unavailable; retain the existing no-service design.

### A26 — Configuration, store, artifact, lock or mutation refusal

- **Current:** missing/locked stores, invalid configuration, unlisted repositories, profile/adapter settings, digest mismatches and immutable mutation conflicts generally return an error; task/store lock waits are bounded, but repairing external prerequisites or old evidence has no unified Director action surface.
- **Not defects by themselves:** unknown events, noncurrent retry identities and already-closed tasks must remain refused, with current-state inspection instead of a forced transition (`internal/interp/engine.go:229-230,322-377`).
- **Evidence:** `internal/cli/advance.go:58-67,262-313,320-325,454-469`; `internal/cli/director.go:147-212`; `internal/interp/engine.go:234-243,497-514,847-860,1109-1118`; `internal/cli/task.go:304-328`.
- **Director needs:** identify caller mistakes versus transient contention versus damaged evidence versus permission/policy boundaries, name the exact source and what did commit, and offer retry, inspect/restore, or Guildmaster/Steward escalation.
- **Small fix:** a shared structured refusal/attention envelope with context-carrying safe commands and preserved underlying diagnostics; never fix mutation conflicts by deleting receipts or silently approving a workflow.

### A27 — Forge transport, unsupported remote or check response

- **Current:** Git/forge calls are individually bounded, but unsupported remotes, authentication/transport failure, unknown check status and more than 100 check contexts return errors; a watcher may retry these permanently with only log output.
- **Evidence:** `internal/forge/github.go:178-213,228-279,283-310,390-409`; `internal/git/git.go:177-197`; watcher behavior in A09.
- **Director needs:** retryable versus unsupported classification, affected head and last successful observation, plus credentials/prerequisite escalation, supported forge configuration or an Artisan fix where applicable.
- **Small fix:** surface typed forge-read failures through A09; implement check pagination separately instead of interpreting incomplete evidence as green.

### A28 — Side effect succeeded, but recording delivery or cleanup was interrupted

- **Current:** publish/merge replay is designed to recover a crash between forge action and recording, retirement preserves unsafe-to-delete work, and an interrupted retirement leaves the task open until re-advance; records or notification errors after merge can therefore leave local progress behind reality.
- **Evidence:** `internal/interp/deliver.go:110-123,231-259`; `internal/interp/workspace.go:51-69`; `internal/forge/github.go:102-143`.
- **Director needs:** remote side-effect status, intended and observed head, last committed local record and preserved-work path, plus idempotent reconcile/re-advance rather than another unguarded merge.
- **Small fix:** report side-effect/recording uncertainty explicitly and expose an inspect/reconcile action; keep existing idempotence and never delete unlanded work to clear a wait.

### A29 — Rebase classification fails before the next review can start

- **Current:** inability to resolve a review base falls back to full scope, but a classifier error aborts before the new binding or attempt is recorded; a pending full-review request deliberately bypasses classification and is an existing escape for this case.
- **Evidence:** `internal/interp/rebase.go:30-70`; full-review request at `internal/cli/task.go:244-254`.
- **Director needs:** old/new ranges, the classifier error and whether a request was consumed, plus `task review --full` followed by advance when classification alone is the obstacle.
- **Small fix:** include this existing escape in the typed refusal rather than making the Director discover it, preserving the accepted-spec and standards bindings and never treating failed classification as proof of rebase-only continuity.

## Wait coverage and existing safe exits

| Interpreter wait | Covered by | Existing exit or missing capability |
|---|---|---|
| `attempt` | A05–A06, A10–A11, A16–A17, A22–A23 | Valid result intake or safe stop/resume/retry; missing recovery for some success/intake and ownership states |
| `decision` | A03–A04, A19 | Actual Guildmaster answer and advance; reconsideration must preserve the old answer |
| `director` | A02–A04, A08, A18 | Latest failed/stopped retry or forge re-advance exists; general alternate route does not |
| `checks` | A01, A09, A27 | Re-read after refresh or external check rerun; conflict/no-check diagnosis and repair route missing |
| `director-stage` | A12, A24 | `step finish`, including `--failed`, or replay of an already written result |
| `paused` | A18, A24 | Resume/ready/complete stop cleanup, then correct advance or retry |
| `done` | A28 | Terminal, not a wedge; no new advance allowed, and quality judging is separate |

The seven wait values are declared at `internal/interp/engine.go:56-76`.
An indefinite human wait is not itself a defect; missing ownership, evidence, a visible condition for resumption or a safe escalation is.
A refusal that protects custody or approvals should remain a refusal, but must identify a lawful next action rather than merely repeat itself.

## Follow-up slicing

1. **Observability first:** A01 and A09–A11/A25, exposing facts, last observations and failed notifications without changing authority.
2. **Minimum route toolbox:** A02 plus A03/A04/A15, with append-only records, new-attempt instructions, per-task holds and preserved abandonment.
3. **Evidence recovery and invariant enforcement:** A05–A07/A14/A16–A17/A20–A21/A29, preserving exact subjects and immutable history.
4. **Operational recovery:** A08/A13/A18–A19/A22–A24/A26–A28, strengthening diagnostics and only then adding supported recovery actions.
5. **Maintenance capture and collection:** every incident above can produce the evidence-backed local ticket and later private Steward queue entry described in [messaging](../messaging.md#maintenance-tickets-to-the-steward-designed-not-built), without blocking a safe current-task recovery on Steward availability.

For each slice, define the behavior and seam first and add executable contract tests for its state transitions, refusal output, duplicate request, partial-write replay and authority boundaries before implementation.
The four D3 cases are useful acceptance scenarios, but this audit deliberately does not claim new red/green test evidence or complete live recovery coverage.
