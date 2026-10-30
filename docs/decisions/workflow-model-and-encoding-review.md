# Workflow model and encoding review

Status: review and recommendations only (task d64).
Nothing here changes a workflow, a step kind, the engine or a digest; each ranked recommendation becomes its own task only if the Guildmaster approves it.

Citations are `file:line` at commit `65023170`, the commit this review was built on.
Store evidence comes from the Director's export recorded as d64's task context (2026-10-30T17:09:53Z, `guildhall task list` and `task timeline --json` over repository `guildhall`).
The reader this review has in mind is the Guildmaster: from the workflow file alone, can they tell what a task will do and where it may stop for them?

## 1. Summary

- **Encoding verdict:** compact and strictly checked, but it answers "what is wired to what", not the Guildmaster's question "who acts, and where will I be asked?". Most of the text is boilerplate (`version` on every step, single-reference arrays, wiring to the obvious producer), and the stops that matter most (merge decisions, loop exits) are not in the file at all.
- **Model verdict:** a linear list with `goto` loops fits the normal route well and should stay. It is not what the engine does, though: loops are dataflow re-runs, `max` is an escalation point rather than a limit, and every real deviation (send-back, retry, design round, rebase, hold, hand merge) lives outside the file, as rails-not-tracks intends.
- **Top recommendations**, in order:
  1. (model, safety) Make `ci_green` fail when deliver has no `validation` input, instead of holding unchecked.
  2. (encoding) Add `guildhall workflow explain`, a plain-text rendering of the effective workflow: who acts at each step, every place the Guildmaster may be asked, and what the engine adds.
  3. (encoding) Lay out the shipped JSON one key per line, with `actor` first; this is digest-free.
  4. (encoding) Say what `max` does wherever it is shown: "ask after N rounds, then every round". Keep the field name.
  5. (model) Refuse the engine-only kinds `rebase` and `design` as workflow steps.

## 2. How a workflow is written today

**The file.** Schema `guildhall.workflow/1` with `schema`, `name`, `description` and `steps` (`internal/workflow/workflow.go:44-50`).
A step is `id`, `type`, `version`, optional `settings` (raw JSON per key), `inputs` (port → array of `"step.output"`) and `on` (outcome → `{goto, max}`) (`workflow.go:52-62`, `workflow.go:91-97`).
Parsing is strict: unknown fields, duplicate keys, trailing data and a newer schema are refused (`workflow.go:156-164`).
The shipped `default.json` is 26 lines, 1540 bytes and nine steps; `bug.json` is 22 lines and 2322 bytes, seven steps, most of it four `instructions` strings (`internal/workflow/default.json:1-26`, `internal/workflow/bug.json:1-22`).

**The registry.** Fourteen kinds, all version 1, are declared in `internal/workflow/registry.go:113-230`: `grill`, `research`, `plan`, `refute`, `document-review`, `guildmaster-approval`, `build`, `rebase`, `design`, `review`, `publish`, `validate`, `deliver`, `handoff-wait`.
Each declares typed ports (`registry.go:16-24`, types at `registry.go:8-14`), allowed settings, the outcomes that may loop (`Loops`) and its actors (`registry.go:55-67`, `registry.go:72-87`).
`rebase` (`registry.go:160-171`) and `design` (`registry.go:172-182`) say in their comments that the engine runs them "never from a workflow", but `Validate` does not refuse them; the only reserved step id is `quality-judge` (`internal/workflow/validate.go:67-69`, `workflow.go:37-39`).

**Validation** (`internal/workflow/validate.go`).

- A kind with several actors requires `settings.actor`, and a kind with one refuses it (`validate.go:112-131`). That is why every `review` carries `"actor": "artisan"` and `grill` carries `"actor": "director"`.
- Each input must reference an earlier step's output of an accepted type; a port takes one reference unless it is `Many` (`validate.go:174-226`). References are always arrays, even for single-reference ports (`docs/configuration.md:339`).
- A loop key must be one of the kind's `Loops`, `goto` must name this or an earlier step, and `1 <= max <= 10` (`validate.go:228-246`, `MaxLoopRounds` at `workflow.go:29-30`).
- List order is run order. There is no branching, conditional step, parallelism or forward jump.

**Digest.** The sha256 of `json.Marshal(def)` (`workflow.go:166-173`).
Formatting and key order in the file do not affect it, nor does writing an omitted optional field as empty (`docs/configuration.md:413-414`); any change to the parsed `Definition` does, including the `description` text, which is a field of the struct (`workflow.go:48`).

**Overrides.** `[repository.workflow_overrides.<step>]` replaces a step's `actor` (`workflow.go:180-219`, `docs/configuration.md:398-409`); the overridden definition has its own digest, which is what the task binds (`docs/tasks.md:70`, `docs/tasks.md:85`).

**Authority.** `Admit` admits a definition that is shipped, approved by exact digest, or meets the review floor and weakens nothing in an authorized baseline (`internal/workflow/authority.go:231-268`).
`Weakenings` matches steps **by id** and guards `review`, `document-review`, `guildmaster-approval`, `validate`, `publish` and `deliver` (`authority.go:25-34`, `authority.go:39-87`).
For a guarded step, removing or reordering it, a weaker actor, any changed setting, a dropped input reference, and a removed, redirected or lowered loop are all weakening (`authority.go:89-119`).
The floor (`FloorProblems`) requires every `deliver` to take a `review` outcome and a `validate` result (`authority.go:132-151`).
An approved digest is admitted **before** the floor is checked (`authority.go:243-249`), so an approved workflow may sit below it, as rails-not-tracks records ("current workflow approval can admit definitions below the structural review floor").

**Retired revisions.** Every shipped change keeps the previous bytes in `internal/workflow/retired/` so bound tasks still resolve (`workflow.go:119-146`); digests are pinned by `TestRetiredRevisionsArePinned` and `TestShippedDigestsArePinned` (`internal/workflow/retired_test.go:11-15`, `retired_test.go:84-85`).

**Task kind → workflow.** `feature` runs the repository's `workflow` (default `default`); `bug` runs `[repository.kinds].bug` (default `bug`) (`docs/configuration.md:136-137`, `docs/tasks.md:42-58`).
The task records no kind: "its bound workflow's name says what it was created as" (`docs/tasks.md:57`).

## 3. How the engine actually runs it

`Advance` (`internal/interp/engine.go:264-330`) takes in the event, closes a task whose pull request was merged outside Guildhall (`engine.go:273-279`), takes pending results and replays validations, runs an open design round **instead of** any workflow step (`engine.go:301-306`), then walks `def.Steps` from the top, passing the dependency gate before the first step not run by the Director (`engine.go:307-320`), and stops at the first step that must wait.
Reaching the end of the list is an error (`engine.go:329`), so a workflow in effect must end with a step that waits.

**The walk restarts from the top, and a step reruns when it is stale.**
There is no program counter.
A finished attempt is stale when its wired inputs (and for a review, its revisions) differ from what it would be given now (`internal/interp/loops.go:188-218`), or when feedback is pending for it (`docs/interpreter.md:477-478`).

**`goto` means "send feedback to this step".**
The target is given the blocking result as `feedback` (`loops.go:233-262`, `docs/interpreter.md:468`, `docs/interpreter.md:488`).
Everything after the target runs again only because its inputs changed: "when a looped build lands a new head, every review after it runs again on that head" (`docs/interpreter.md:481`).
So `validate … "failed": {"goto": "build"}` also re-runs review, publish and validate, though the rule names none of them.

**`max` is not a limit: it is the round after which the Guildmaster is asked, and then asked every round.**
The allowed count is `max` plus one for each loop exit answered `continue` (`loops.go:220-223`).
Past it the engine opens `<step>-x<round>`, kind `loop-exit`, with `continue`, `accept`, `reject`, or only `continue` and `reject` for validate (`loops.go:39-43`, `loops.go:396-442`).
`continue` buys exactly one more round, so after the first exit the Guildmaster is asked on every further blocking result: d32 shows `validate-x4`, `x5` and `x6`, not one question every three rounds.
(The research notes said "every N rounds"; the code and the store both say every round after the first N.)
A contested Director ruling opens the exit even within `max` (`docs/interpreter.md:508`), and a blocking outcome with no rule waits for the Director (`loops.go:358-368`).

**The two delivery gates read the file differently.** This is a safety finding, not only a readability one.

- The **review gate** reads step **order**: every `review` and `document-review` step earlier than publish or deliver must approve the head (`loops.go:641-671`; `reviewOutcomes`, `internal/interp/deliver.go:955-980`; `docs/interpreter.md:546-553`).
  Deliver's and publish's wired `review` inputs feed only the pull request text and the floor check (`deliver.go:192-208`, `authority.go:143`).
  The file looks as if it says which reviews deliver depends on; the engine checks every earlier review, which is stricter, so this half is safe.
- The **validation gate** reads **wiring**: with no `validation` input, `validated` returns nil (`internal/interp/validate.go:553-556`), and `ci_green` then holds unchecked (`deliver.go:400-408`).
  `docs/configuration.md:165` says `ci_green` holds when "Deliver's `validation` input is a `passed` result", which is literally true and silent on the missing case.
  Only the floor stops a workflow leaving the input out, and the floor does not apply to an approved digest (§2).
  The repository's own test fixture `internal/interp/testdata/workflows/three-reviews.json:24-25` is such a workflow: deliver takes three reviews and no validation.
  No real task is known to have merged this way (§5), but nothing in the engine prevents it.

## 4. Hidden behaviour

Behaviour the engine adds that the workflow file does not show.
*Should the file show it?* is judged by the Guildmaster's test: does knowing it change what they expect the task to do, or where it may stop for them?

| Behaviour | Where it lives | Visible today | Should the file show it? | Why |
|---|---|---|---|---|
| Design rounds: a Director tool before the first build, designer Artisan, Guildmaster decision `design-<n>`, can make plan stale | `engine.go:301-306`, `internal/interp/design.go:140`, `docs/tasks.md:305-337`, `docs/glossary.md:87-90` | No; eligibility is inferred from shape (a Director `grill` and a `build`) | Partly | It is a Guildmaster stop, so where it can happen should be shown, but it is a Director choice, not a route step (rails-not-tracks). Show eligibility, rendered (rec. E1), not a step. |
| Rebase steps `<step>-rebase` and `-rebase-review`, tiered rebase, the merge queue, automatic send-back on `major_conflicts` | `registry.go:160-171`, `docs/interpreter.md:303-405`, `docs/step-send-back.md:5` | No | Partly | 16 of 53 done tasks ran one (§5); the Guildmaster should know review can repeat after merge-time rebases. A rendered note suffices; the queue is repository-wide, not per workflow. |
| Merge decision `deliver-<k>` and who must approve, from `[repository.merge.requires]` | `docs/configuration.md:148-183`, `registry.go:214-215` | No; deliver looks unconditional | Yes, rendered | It is the Guildmaster's main stop, and the same workflow asks or not depending on config (§5). It must stay in config ("No workflow can change a repository's merge requirements", `docs/configuration.md:183`), so show it in the rendering, not the JSON. |
| Loop-exit decisions after `max`, and Director-ruling disputes within it | `loops.go:396-442`, `docs/interpreter.md:503-513` | Partly: `max` is visible, the decision is not | Yes | The file states a number whose meaning (a Guildmaster stop) is invisible; recs. E1 and E3. |
| Dependency gate before the first non-Director step | `engine.go:307-320`, `docs/interpreter.md:120-153` | No | No | A property of the task graph, not the workflow; `task show` already reports it. |
| Engine-added inputs: `feedback`, `previous`, `rulings`, `reviewed`, `resolution`, task `context`, `design`, retry context, task grants | `docs/interpreter.md:463-475`, `loops.go:233-262` | No; an engine `context` and a wired `context` share one name (`docs/interpreter.md:469`) | No | They are the engine's lineage, not route; listing them in every file would be noise. Document them once per kind (rails-not-tracks "Purpose"). |
| Role profiles, resolved at use and outside the digest | `internal/workflow/defaults.go:9-35`, `docs/decisions/role-profiles-outside-the-digest.md` | No, by design | No | Deliberately the copy's runtime choice; the rendering may show the effective profile. |
| Per-repository actor overrides | `workflow.go:180-219` | No; the file's `actor` may not be the one that runs | Partly | The digest already covers it; the rendering should show the effective actor, which is what E1 renders. |
| Director send-back to any earlier Artisan step; retries of failed attempts | `docs/step-send-back.md`, `docs/running.md` (`step retry`) | No | No | Director moves on the rails; recording them per task (not in the file) is rails-not-tracks' stated design. |
| A PR merged outside Guildhall closes the task as done | `engine.go:273-279`, `docs/tasks.md:168-198` | No | No | An external event, not route. |
| Quality judge after delivery, reserved id `quality-judge`; quality audit decisions | `workflow.go:37-39`, `docs/decisions/quality-judge.md` | No | No | After delivery and outside the task's route; it asks the Guildmaster (`audit` in 8 tasks, §5), so the rendering may mention it. |
| Publish comes before validate so CI has a pushed head | `docs/configuration.md:385` | Order yes, reason no | Partly | The reason is purpose text, which rails-not-tracks wants per kind; not a file field. |
| Review gate covers every earlier review, whatever deliver wires | `loops.go:641-671`, `deliver.go:955-980` | No; the wiring suggests otherwise | Yes | The file should not imply a weaker gate than the real one; E1 renders it. |
| Validation gate depends on wiring | `internal/interp/validate.go:553-556` | Technically yes | Yes, and fixed | The file shows it only by omission; rec. M1 removes the hazard. |
| A changed `.guildhall/policies.md` forces `guildmaster_approval` | `docs/configuration.md:177-180` | No | No | A repository safety rule; the merge question already says why. |

## 5. Evidence from real tasks

Source: the Director's store export in d64's context (`guildhall task list` and `task timeline --json`, repository `guildhall`, 2026-10-30T17:09:53Z).
This attempt could not read the store or the Director's config directory itself (outside its sandbox), so every count below is taken from that export, and counts it cannot support are marked.

**Workflows bound.** 70 tasks, d3–d72: 53 done, 8 cancelled, 9 open.

| Workflow and digest prefix | What it is | Tasks |
|---|---|---|
| `dogfood@19df92ab` | The Guildhall repository's own workflow (three reviews) | d3–d45 (43) |
| `default@30e722b7` | Shipped default, now retired as `retired/default-a10f8e3a.json` (`retired_test.go:13`) | d46, d47 |
| `default@749fc781` | Current shipped default (`retired_test.go:84`) | d52–d64, d66, d68, d69, d70 (17) |
| `bug@f8e77aaf` | Current shipped bug (`retired_test.go:85`) | d48–d51, d65, d67, d71, d72 (8) |

The live `dogfood` file was not readable from this attempt.
The export lists its review attempts under one id, `review`, so this review cannot confirm the live file's step ids; `docs/interpreter.md:553` and the fixture `internal/interp/testdata/workflows/three-reviews.json` are the documented shape (evidence partial).
It is a real variant: a near-copy of the default that differs in its review steps, with its own digest and approval.

**Findings and the tasks behind them.**

- **`max` behaves as an escalation point.** 5 loop-exit decisions, in 2 tasks, all on validate, all answered `continue`: d32 (`validate-x4`, `x5`, `x6`: 6 failures against `max` 3) and d42 (`validate-x4`, `x5`: 5 against 3). No review or acceptance loop ever reached its exit; d45 used exactly its 3 acceptance rounds. The Guildmaster never chose anything but "keep going".
- **Acceptance loops within `max`.** 13 `changes_requested` answers on `accept`, in 10 tasks: d8, d11, d13, d14 (2), d23, d31, d45 (3), d57, d63, d69. The loop fits these.
- **Re-acceptance after an approved spec.** d8 (`accept-2` and `accept-3` both approved), d14 (`accept-3`, `accept-4`) and d41 (`accept-1`, `accept-2`) were asked to accept again after approving. The file has no route back to acceptance after approval; plan was made stale from outside (send-back or re-plan). The cause is inferred: the export does not name it.
- **Send-backs.** Not counted: the export does not tell a send-back attempt from a loop or stale re-run. The re-acceptances above are the visible trace.
- **Failed attempts.** 25 failed attempts in 14 tasks (build 13, review 10, research 1, grill 1): d3, d4, d8, d12, d20, d23, d25, d27, d31, d41, d45, d49, d52, d58. Every one but d58 (cancelled) was followed by another attempt of the same step. d23 ran build 14 times (3 failed, 2 stopped). No rule in the file bounds or shows these.
- **Stopped attempts.** 21 in 13 tasks: d4, d8, d11, d14, d22, d23, d32, d38, d39, d40, d55, d56, d66; d38 and d39 were cancelled after a stopped grill.
- **Design rounds.** None: no task has a `designer` attempt. `task design` landed at `604d0b4a` (#209); d14, which built it, ran plan 5 times and acceptance 4 times, the pattern design rounds were added for.
- **Rebase steps.** 16 of 53 done tasks ran at least one synthetic rebase step: d14, d20, d32, d33, d45, d46, d47, d52, d53, d54, d55, d56, d57, d63, d66, d71. On the current default, 8 of its 9 done tasks did (all but d68). None of these steps is in any workflow file.
- **Merge decisions come from config, not the file.** d3–d45 (`dogfood`) record `deliver-<k>` decisions, including holds in d4, d22, d23 and d28, and ten tasks with two or more merge decisions as heads changed (d3, d4, d6, d8, d13, d16, d17, d18, d22, d23). From d46 on, no task records a merge decision at all, under the same shipped workflows. The likely cause is a change to the repository's merge requirements (inferred; the config was not readable). Either way, the same file stops or does not stop for the Guildmaster at merge, and the file cannot say which.
- **Merged outside Guildhall.** d51 and d65 (both `bug`) were merged by hand (PR #194 / #196) and closed as done. Two tasks landing in one hand-made change is a route the model cannot express.
- **Other Guildmaster stops not in the file.** Quality audit decisions (`audit=yes`) in 8 tasks: d6, d9, d18, d20, d30, d48, d52, d66. A task grant decision in d49 (`grant-g1`).
- **Bug workflow fit.** 7 of 8 bug tasks are done, and five (d50, d51, d65, d67, d71) recorded no Guildmaster decision at all, as `bug.json` intends.

## 6. Model review

Each answer is measured against [rails, not tracks](rails-not-tracks.md): the workflow is the normal route, the engine enforces invariants regardless of route, the Director chooses a safe route when circumstances change, per-task route changes are recorded beside the pinned revision, and there is no executable workflow code.

### (a) Per-task add or skip of steps, compared with separate files such as `bug.json`

Separate files work today and the evidence does not ask for more: 8 bug tasks ran as designed, and `TestBugIsTheDefaultWithoutAcceptance` pins `bug.json` as the default minus grill and accept.
Their cost is duplication: `default`, `bug`, `dogfood`, `human-review` and `refute-trial` repeat the same publish, validate and deliver lines, and each has its own digest and approval.
Per-task **skip** of a guarded step is weakening by definition (`authority.go:74`), so it would need a Guildmaster approval per task; skipping an unguarded step (research) saves little.
Per-task **add** of a built-in step is what rails-not-tracks names ("add a tested built-in step") and is non-weakening under `Weakenings` if it keeps order.
d69 plans per-task step settings; its design is still at acceptance (`accept-2` open in the export) and is treated here as planned, not built.

**Recommendation:** keep separate files now.
Revisit per-task *add* only after d69 lands, using d69's per-task record rather than a second mechanism, and only as a composed definition that is admitted by `Admit` and bound by digest (rec. M5).
Do not add per-task skip.

### (b) Should Director-manual moves be workflow vocabulary?

Design rounds, send-back, retry, rebase, hold, dependency override, grants and context are each a Director tool or engine feature with its own command and record.
Putting them in the file would turn rails into tracks: the Director's choice would become a scripted branch the Guildmaster must approve per workflow, and a workflow that omitted one would forbid a safe move.
What the file lacks is not the moves but **where they may happen and where they stop for the Guildmaster**.

**Recommendation:** keep the moves outside the file; show their eligibility and stops in a rendering (rec. E1).
One inference is fragile: design-round eligibility is read from the file's shape (a Director grill and a build, `design.go:140`), so a feature workflow overridden to an Artisan grill silently loses design rounds; this is left as open question 2.

### (c) Fixed `max`, or "on exhaustion, hand the decision to the Director"?

`max` already is "on exhaustion, ask" (§3); it only asks the Guildmaster, not the Director.
The store shows 5 of 5 exits answered `continue`, all on validate, where there is nothing to accept and `reject` only hands the task to the Director anyway (`loops.go:425-427`).
For review and acceptance exits, `accept` records a Guildmaster approval (`docs/interpreter.md:491-495`), which the Director may never grant (rails-not-tracks, "Merge authority"); those must stay with the Guildmaster.
Renaming `max` would change the canonical struct and so every digest and approval, for a word.

**Recommendation:** keep the field and its name; say what it does wherever it is shown (rec. E3).
Separately, consider letting the Director answer a **validate** loop exit's `continue` with a recorded reason (rec. M2); that is an authority change and needs the Guildmaster's explicit decision.

### (d) Linear steps plus `goto`, or an explicit state machine or graph?

The engine is already a dataflow over a topologically ordered list: inputs only from earlier steps (`validate.go:196-212`), staleness re-runs downstream steps, `goto` injects feedback.
An explicit graph would add expressive power nothing in §5 asks for (no task needed parallel steps or a forward branch), and it would break the authority classifier, which matches by id and **order** (`authority.go:52-66`): every graph shape would need its own approval or a new classifier.
A state machine would make the hidden transitions explicit but would duplicate what staleness derives, and the transitions the store shows outside the file are exactly the Director's route choices, which rails-not-tracks keeps out of it.

**Recommendation:** keep the model.
Describe `goto` as "send feedback to" in docs and rendering, and render the re-run chain ("validate failed → build, then review, publish, validate run again").

## 7. Encoding review

Measured on `internal/workflow/default.json`.

| Observation | Measure | Can it change without touching the digest? |
|---|---|---|
| `"version": 1` on every step | 9 of 9 steps | Not removed: "a task never silently changes step-kind version" (`docs/configuration.md:337`). It must stay written in the file. |
| `id` equal to `type` | 8 of 9 steps (all but `accept`) | Yes, if an omitted `id` is filled from `type` at parse, before hashing (rec. E4). Ids still matter: authority, decisions and attempts are named by them. |
| Single-reference arrays | 11 of 12 wired ports (13 references; only `plan.context` has two) | Yes, if a string is read as a one-element array at parse (rec. E4). (The research notes said 12 of 14; recounted here.) |
| Repeated wiring | `accept.spec` ×3; `build.head`, `publish.head`, `review.outcome` ×2 | Defaulting wiring to "the latest producer of that type" would make the meaning depend on order, hide what the floor checks, and change the struct. Not recommended. |
| Actor buried in `settings` | `"settings": {"actor": "artisan"}` mid-line on `review`; `director` on `grill`; Guildmaster stops only as `guildmaster-approval` | Layout yes (rec. E2); the effective actor after overrides needs a rendering (rec. E1). |
| Guildmaster stops that cannot be seen | Merge decisions (config), loop exits, design rounds, audit | Not in JSON; rendering (rec. E1). |
| Prose in JSON strings, no comments | `bug.json` is 2322 bytes, mostly four one-line `instructions` | Only with a front end (rec. E6). |
| `description` unchecked | Restates the step list in prose; part of the digest (`workflow.go:48`); not weakening (`authority.go:50`) | No: editing it changes the digest (rec. E7). |

**Alternative syntax.** Any of YAML, TOML, a small DSL or a shorthand is judged as a **front end** that normalises to today's `Definition` before hashing, so digests and approvals are kept.
TOML is the cheapest: the config is TOML already (`docs/decisions/0008-toml-parsing-for-copy-configuration.md`), and it gives comments and multi-line strings.
YAML adds a dependency and implicit typing for little gain; a DSL adds a grammar to own and an "executable" look rails-not-tracks warns against.

**Struct changes and their migration cost.** A change to `Definition` itself (renaming `max`, a `purpose` field, an implicit `version`, references as scalars in the canonical form) changes the digest of every workflow:

- shipped `default` and `bug` need retired copies (`retired_test.go:77-98`);
- every Guildmaster approval, keyed by exact digest, is void, so each custom workflow (here `dogfood`) needs approving again;
- retired copies must still parse under the old struct, so the parser keeps both forms;
- an in-flight task resolves its bound digest by recomputing it from the file (`docs/tasks.md:85`), so changing how digests are computed would refuse its next advance unless the old computation is kept.

None of this review's encoding recommendations changes the struct.
One considered and rejected: `instructions_file` (prose in a side file) would put reviewer-facing text outside the digest, so a workflow could change after approval; it fails the second hard constraint.

## 8. Comparison

These are reference points, not targets.

**State machines.** A state machine names every state and transition, so the Guildmaster could read each stop.
Guildhall's real transitions come from three sources the file does not hold: dataflow staleness, the Director's recorded route choices and repository config.
A state-machine encoding would either omit them, and look complete when it is not, or include them, and turn Director choices into scripted tracks.
What is worth borrowing is the *view*: render the reachable stops (rec. E1).

**GitHub Actions.** Jobs declare `needs:` (an explicit DAG), `if:` conditions and reusable workflows called with inputs.
`needs:` is what Guildhall's `inputs` already are, with order standing in for the DAG.
`if:` is the per-task skip that §6(a) rejects for guarded steps.
Reusable workflows are the analogue of "bug is the default minus two steps": a later option for the duplication in §6(a), but each composed result would still need its own digest and admission, so it saves typing, not approvals.

**Temporal.** Workflows as code give unlimited flexibility and replay, and are ruled out by rails-not-tracks: "without introducing executable workflow code".
Guildhall already gets replay from its store and deterministic `Advance`.

## 9. Ranked recommendations

Each item is one pull request and can be taken alone.
"Model" changes what a workflow means or what the engine does with it; "encoding" changes only how it is written or shown.
Every item is checked against the two hard constraints: **(C1)** no task silently changes step-kind version, and **(C2)** every running workflow is a Guildmaster-approved one identified by digest.
"Approved" in C2 is read as today's `Admit` reads it: shipped, approved by digest, or admitted as non-weakening of one of those.

### 1. M1 — `ci_green` fails when deliver takes no validation (model, safety)

Before (`default.json:23-24`, verbatim), and the engine path that matters when `"validation"` is left out:

```json
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["publish.head"], "review": ["review.outcome"], "validation": ["validate.result"]}}
```

```go
// internal/interp/validate.go:553-556
	if len(deliver.Inputs["validation"]) == 0 {
		return nil
	}
```

After (sketch, engine only): with `ci_green` required and no `validation` input, the condition fails with "deliver step takes no validate result", the floor's own wording; a repository that runs no CI says so with `no_ci`, or turns `ci_green` off in its config, where the Guildmaster can see it.

- **Benefit:** closes the gap rails-not-tracks names ("current delivery gates depend on the steps and inputs present"); the validation invariant no longer depends on wiring or admission route.
- **Cost:** a few lines in `validated`, a test per case, and a docs line at `docs/configuration.md:165`.
- **Risk:** a Guildmaster-approved workflow below the floor, with `ci_green` required, stops merging by itself and waits with a stated reason. No task in §5 is known to use one.
- **Digests and authority:** no digest changes; strictly tightens what deliver demands.
- **C1:** no kind version changes. **C2:** unchanged; it narrows what an approved workflow can do.

### 2. E1 — `guildhall workflow explain` (encoding: presentation)

Before: the Guildmaster reads `default.json` and must know that `guildmaster-approval` stops for them, that `max` means a later question, and that merge stops live in config.

After (sketch of output for the default, with `merge.requires` read from config; here every requirement is on, the default):

```text
default sha256:749fc781…  (feature tasks in repo guildhall; overrides: none)
 1 grill     Director, with you live
 2 research  Artisan (researcher)
 3 plan      Artisan (planner)                  design rounds: Director may ask before build; you decide each
 4 accept    YOU approve the spec               changes requested → plan; after 3 rounds you are asked, then every round
 5 build     Artisan (builder)
 6 review    Artisan (reviewer)                 changes requested → build, then review runs again; ask after 3
 7 publish   engine: push and open the PR
 8 validate  engine: CI on the pushed head      failed → build, then review, publish, validate run again; ask after 3
 9 deliver   engine: merge                      merge needs: YOUR approval, artisan approval, CI green, nothing open
           review gate: every review above (review); validation: validate
           engine may add: rebase + rebase review at validate or merge; send-back, retry (Director)
```

- **Benefit:** answers the Guildmaster's question directly, for the *effective* workflow (overrides and config applied), which no file edit can do.
- **Cost:** a read-only CLI subcommand next to `workflow approve` / `approvals` (`internal/cli/workflow.go`), plus tests; the text could also go into `workflow approve`'s prompt and `task show`.
- **Risk:** the rendering drifting from the engine; mitigate by building it from the registry, `loops.go` options and `config.MergeRequires`, not hand-written text.
- **Digests and authority:** none.
- **C1, C2:** unaffected; read-only.

### 3. E2 — Lay out the shipped JSON one key per line, actor first (encoding)

Before (`default.json:15-17`, verbatim):

```json
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
```

After (sketch; parses to the same `Definition`):

```json
    {
      "id": "review", "type": "review", "version": 1,
      "settings": {"actor": "artisan"},
      "inputs": {
        "spec": ["accept.spec"],
        "head": ["build.head"]
      },
      "on": {"changes_requested": {"goto": "build", "max": 3}}
    },
```

- **Benefit:** who acts and what loops back are on their own lines; diffs touch one line per change.
- **Cost:** reformat `default.json`, `bug.json` and the two examples; check no test compares the shipped bytes rather than the digest (`TestShippedDigestsArePinned` compares the digest).
- **Risk:** small; the file roughly doubles in lines.
- **Digests and authority:** none: formatting is outside the digest (`docs/configuration.md:414`), so no retired copy is needed.
- **C1, C2:** unaffected.

### 4. E3 — Say what `max` does (encoding: wording)

Before (`docs/configuration.md:340`, `docs/configuration.md:347`): "`{"goto": <step-id>, "max": <rounds>}`" and "`max` must be between 1 and 10, so every loop is bounded".

After (sketch): "`max` is the number of rounds before the Guildmaster is asked whether to continue; each `continue` allows one more round, so after `max` they are asked every round. Validation keeps it between 1 and 10."
The same wording goes into the loop-exit question and E1.

- **Benefit:** the file's one visible stop number reads as what it is; matches d32 and d42.
- **Cost:** docs and message text.
- **Risk:** none known.
- **Digests and authority:** none; the field name stays.
- **C1, C2:** unaffected.

### 5. M3 — Refuse the engine-only kinds in workflow files (model)

Before (`registry.go:166-171`, verbatim excerpt): `Type: "rebase", Version: 1, … Actors: artisanOnly,` — with a comment saying "never from a workflow", and nothing in `Validate` enforcing it (`validate.go:67-69` reserves only `quality-judge`).

After (sketch): `Kind` gains an engine-only mark on `rebase` and `design`; `Validate` refuses them as workflow steps, as it refuses `quality-judge`.

- **Benefit:** the registry stops offering two kinds a workflow must not name; removes the id collision that today forces design-round ids to take a `round-` prefix when a workflow has a step named `design` (`docs/tasks.md:336`).
- **Cost:** one field and a validation rule, with tests.
- **Risk:** a Guildmaster-approved custom workflow that names `rebase` or `design` becomes invalid at its next advance; none is known.
- **Digests and authority:** no digest changes; tightens what validates.
- **C1, C2:** unaffected.

### 6. E4 — Shorthand normalised at parse: scalar references and default `id` (encoding)

Before (`default.json:20-22`, verbatim):

```json
    {"id": "validate", "type": "validate", "version": 1,
     "inputs": {"head": ["publish.head"]},
     "on": {"failed": {"goto": "build", "max": 3}}},
```

After (sketch; the parser fills `id` from `type` and wraps the string in an array before hashing, so the digest is the same as before):

```json
    {"type": "validate", "version": 1,
     "inputs": {"head": "publish.head"},
     "on": {"failed": {"goto": "build", "max": 3}}},
```

- **Benefit:** removes 8 of 9 `id`s and 11 of 13 brackets in the default; the canonical form is unchanged.
- **Cost:** a custom decoder for `Refs` and an id default in `Parse`, checked to keep `strictjson`'s refusals; docs.
- **Risk:** two spellings of one thing; a second step of the same type must name its id (validation already reports duplicate ids).
- **Digests and authority:** none for any existing file; a shorthand file hashes as its expanded form.
- **C1:** `version` stays required and written. **C2:** unaffected.

### 7. M2 — Let the Director answer `continue` at a validate loop exit (model)

Before: `validate-x<k>` asks the Guildmaster `continue` or `reject` (`loops.go:39-43`, `loops.go:405-408`); d32 and d42 were answered `continue` five times out of five.

After (sketch): at a validate exit the Director may record `continue` with a reason (for example a known flaky check); `reject`, and any review or acceptance exit, stay with the Guildmaster. A repository setting could cap Director continues before the Guildmaster is asked.

- **Benefit:** removes Guildmaster interruptions that carried no decision; CI must still pass, so no invariant is weakened.
- **Cost:** a decision-answer path for the Director, a cap, messages and tests.
- **Risk:** unbounded CI spend if the cap is missing; moves an answer from the Guildmaster to the Director, which the Guildmaster must approve as policy.
- **Digests and authority:** no digest change; an **authority change**, so it needs the Guildmaster's explicit approval before it is built.
- **C1, C2:** unaffected.

### 8. E6 — A TOML front end for workflow files (encoding)

Before: `bug.json:6` is one 280-character line of `instructions`, with no room for a comment saying why.

After (sketch; parsed to the same `Definition` as `bug.json`):

```toml
schema = "guildhall.workflow/1"
name = "bug"

# No Guildmaster stage: research reproduces, build tests first.
[[steps]]
id = "research"
type = "research"
version = 1
settings.instructions = """
Reproduce the defect the task title describes and find its cause. \
If you cannot reproduce the defect, or the behaviour is not a defect, \
finish with status failed: put what you tried and what you found in the summary, \
and build nothing on a guess."""
```

- **Benefit:** comments and readable prose for instruction-heavy workflows.
- **Cost:** a second parser and strictness rules, `workflow approve --file` for `.toml`, docs; shipped files could stay JSON.
- **Risk:** two formats to read and review; comments are outside the digest, so a comment can drift from the behaviour without any approval.
- **Digests and authority:** none if normalised to `Definition` before hashing.
- **C1:** `version` stays required. **C2:** unaffected.

### 9. E7 — Make `description` state purpose, not restate steps (encoding)

Before (`default.json:4`, verbatim): `"description": "Grill and research, plan, Guildmaster acceptance, build, independent automated review with bounded fix rounds, publish, validation, delivery under the repository's merge policy."`

After (sketch): `"description": "Turn an accepted spec into a merged change that an independent reviewer approved and CI passed, asking the Guildmaster to accept the spec and, if the repository requires it, the merge."`

- **Benefit:** the first step of rails-not-tracks' deferred "Purpose, not only procedure"; the step list is left to E1, which cannot drift.
- **Cost:** new shipped digests, a retired copy of each changed file and pinned-digest updates (`retired_test.go`).
- **Risk:** low; description changes are not weakening (`authority.go:50`), but a copy whose own workflow is edited the same way needs approving again, or is admitted as non-weakening.
- **Digests and authority:** changes the shipped digests (new release revision; bound tasks keep the retired one).
- **C1:** no kind version changes. **C2:** new tasks bind the new shipped digest, authorized by the release.

### 10. M5 — Per-task added steps, after d69 (model, deferred)

Before: a task can only follow its bound file; rails-not-tracks' "add a tested built-in step" has no tool.

After (sketch): a per-task route record, beside the bound revision and using d69's per-task record, adds a built-in step at a position; the engine composes bound definition + record into a new definition, admits it with `Admit` against the bound revision as baseline, records its digest on the task, and runs that.
Removing or moving a guarded step is refused, as `Weakenings` already classifies it.

- **Benefit:** a stronger review or a refutation for one risky task without a new workflow file.
- **Cost:** a route-record contract, composition, admission per change, `task show` output.
- **Risk:** composition rules are new authority surface; must wait for d69 so the two per-task mechanisms are one.
- **Digests and authority:** each composed definition has its own digest, admitted as non-weakening or approved explicitly.
- **C1:** added steps name their version. **C2:** the running definition is the composed one, identified by its digest and admitted by `Admit`.

## 10. Open questions for the Guildmaster

1. **Approval for M2.** Should the Director ever answer a loop exit, even validate's `continue`? If not, drop M2; E3 still applies.
2. **Design-round eligibility.** It is inferred from the workflow's shape (a Director grill and a build), and the task records no kind. Should the task record its kind at `task new` so eligibility reads the kind, or should a workflow say so explicitly (a struct change, so new digests)?
3. **C2 and non-weakening admission.** Is "Guildmaster-approved" satisfied by today's non-weakening admission (shipped or approved baseline, nothing weakened), as this review assumes for E7 and M5, or must every running digest carry an explicit approval?
4. **The live `dogfood` workflow.** It was not readable here. Should a follow-up attach it, so that E1's rendering and M1's check can be confirmed against the Guildhall repository's real three-review file?
5. **Merge decisions after d45.** No task since d46 records a merge decision. Was that a deliberate change to `merge.requires`? If so, it is the clearest case for E1: the same workflow file now means a different set of stops.
