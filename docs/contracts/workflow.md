# `internal/workflow`

## Contract

`Parse`, `Validate`, the step registry with each kind's allowed actors, `Step.Actor`, actor overrides and digests, and the review outcome shared by every review and document review, bound to its subject (R1.60, `docs/decisions/actor-as-a-step-setting.md`).

`JudgeStep`, `quality-judge`, is reserved for a delivered task's [judge attempts](../decisions/quality-rubric.md): `Validate` refuses it as a step id.
The `profile` and `skills` settings and their `Step` accessors follow the per-kind support described in [configuration](../configuration.md#built-in-step-kinds).
Invalid types, invalid names, duplicate skills and undeclared settings are refused during validation; omitted settings remain valid.
Grill steps accept optional `skills` with the shared distinct-name-list validation for either allowed actor, without adding `profile` or `instructions` ([decision](../decisions/grill-live-interview.md)).
The release skills for a grill follow its actor: `guildhall-grill` with `guildhall-domain-model` for the Director, `guildhall-grill-unattended` for an Artisan ([decision](../decisions/grill-live-interview.md)); the registry does not bind skills to actors, so a step lists the ones it renders, and the shipped default's Director grill lists `guildhall-grill` and `guildhall-domain-model` (`TestDefaultGrillIsTheDirectorsLiveGrill`, red first against the default with no skills, [red run](../evidence/director-stages-2026-10-22-red.txt)).
`Default` and `Bug` return the shipped definitions, embedded from `default.json` and `bug.json`: `bug` is the default's steps without `grill` and `accept`, so it has no Guildmaster stage, with plan's context `research.notes` only, build and review taking `plan.spec`, and `instructions` on research (reproduce the defect from the task title and find its cause, or fail with what was tried and found), plan (no acceptance step: take the recommended answer to an ordinary open question and record it, or fail with a question that needs the Guildmaster), build (a regression test first, failing before the fix and passing after it, named in the commit) and review (check that test), the workflow of a [bug task](../tasks.md#task-kinds) ([decision](../decisions/task-kinds.md)).
`Hallmark` returns the shipped `hallmark` workflow, embedded from `hallmark.json`: one `hallmark` step under the `researcher` profile with the `guildhall-hallmark` skill and read-only instructions, and no deliver step, the workflow of a [hallmark task](../tasks.md#task-kinds) ([decision](../decisions/report-only-workflows.md)).
The `hallmark` step kind is Artisan-only, takes optional single `previous` and `baseline` documents, which the engine supplies, writes a `report` document and accepts `profile`, `skills` and `instructions` (`TestHallmarkKind`, `TestHallmarkWorkflow`).
`OwnOverrides` returns the overrides naming a step a definition has, which a workflow other than a repository's feature workflow takes.
`Weakenings` reports a `publish` or `deliver` step of a type its baseline has none of, so the guard-less `hallmark` baseline admits no delivering revision (`TestHallmarkBaselineAdmitsNoDelivery`).
`Retired(name)` returns the earlier releases' shipped definitions of that name, embedded as their exact bytes from `retired/<name>-<commit>.json`, parsed and validated; they keep resolvable a task bound before the shipped bytes changed and are not shipped by this release (`TestRetiredRevisionsArePinned` pins each digest and `TestShippedDigestsArePinned` the current shipped ones, [decision](../decisions/retired-shipped-revisions.md)).
`research`, `plan` and `build` accept `instructions` ([decision](../decisions/task-kinds.md)); `research` still refuses `skills`.
An input `Port` may list `Also`, the other output types it accepts besides `Type`, checked by `Port.Accepts`; only the `spec` input of `build`, `review` and `rebase` sets it, accepting a `document` as well as an `accepted-spec`, and every other mismatch is refused with the same error as before.
`Kind.Output` returns a kind's output port by name, so the engine can gate a spec by its source's output type.
`RoleProfile` maps a step type to its role profile name: `research` to `researcher`, `plan` to `planner`, `build` to `builder`, `review` to `reviewer` and `rebase`, the interpreter's synthetic rebaser step, to `rebaser`, and every other type to none.
`EffectiveProfile(step)` is the profile a step runs under: the `profile` it names, else its type's role profile when an Artisan runs it, else none; a step overridden to the Guildmaster gets none.
It reads the step only, so the role profile is never written into a definition and is outside its digest ([configuration](../configuration.md#profiles), [decision](../decisions/role-profiles-outside-the-digest.md)); every reader of a step's profile, admission, the profile-settings check, launch, rendering, the role prompt and the narrowed rebase review, calls it.
`ApplyProfileDefaults(def, declared)` returns a copy of `def` in which each step run by an Artisan, with no `profile` setting and a research, plan, build or review type whose role profile `declared` reports declared, names that profile; an explicit profile always wins, a step overridden to the Guildmaster gets none, and `def` is not modified.
It is kept only to recognise the legacy digest of a task bound while role profiles were written into the definition, which the CLI's bound-workflow resolution accepts besides the bare digest.
`ValidateStepSettings` checks an isolated step's kind, version and settings with the same actor, allowed-key, required-setting, type, name and duplicate rules as `Validate`, without requiring the surrounding workflow's input and loop wiring.

`ReviewOutcome.Validate` and `ReviewOutcome.BoundTo` enforce the [review outcome revision contract](../configuration.md#review-outcome).
`ParseReviewOutcome` reads `guildhall.review-outcome/2` and `/1`; a /1 document is decoded with the /1 shape, so a /2-only key is refused, and returned with the /2 meaning (rule-less findings as suggestions, `still-open` as `unresolved`, `withdrawn` as `resolved`), its schema kept; `/3` is `ErrNewerSchema`.
Review outcomes carry a one-paragraph `summary` (required in /2), blocking `findings` each with a stable `id` and a cited `rule`, `suggestions` with ids unique across both lists, and `dispositions` of the previous round's findings, a `disputed` one with a `reason`; `ReviewOutcome.Blocking` is true exactly for `changes_requested`, whatever its findings cite, for either version; suggestions alone belong in an `approved` outcome.
`DispositionState.Valid` is the one disposition-state rule, used by `ReviewOutcome.Validate` and, through `Normalized` for the /1 names of old projections, the store's review projection; `DispositionState.Open` (unresolved or disputed) is the test for a finding that still stands.
Their `unknown state` refusal tests (`review_test.go`, `internal/store/rulings_test.go`) pin it, and `TestParseReviewOutcomeV1` and `TestBlockingAcrossVersions` pin the /1 reading.

`Weakenings`, `FloorProblems` and `Authority.Admit` are the workflow-edit authority policy at task creation ([configuration](../configuration.md#enforcement-at-task-creation)): a conservative classifier of a revision against a baseline, the review floor, and the admission decision over the shipped definitions, the caller's approved digests and candidate baselines; they read and record nothing.

## Refutation contract

The [built-in registry](../../internal/workflow/registry.go) owns `refute` and `guildmaster-approval` ports, settings, actors and loop outcomes.
Missing required inputs, mismatched artifact types and unsupported actors/settings are refused by the existing validator.
See [configuration](../configuration.md#opt-in-plan-refutation) for opting in, the [baseline stage guide](../../baseline/stages/refute.md) for critique instructions, and the [interpreter contract](../interpreter.md#plan-refutation) for intake and acceptance semantics.

## Tests

`workflow_test.go`, `strict_test.go`, `review_test.go`, `actor_test.go`, `profile_test.go`, `grill_test.go`, `defaults_test.go`, `bug_test.go`

`TestBugIsTheDefaultWithoutAcceptance` checks that `bug` validates and has the default's steps without grill and accept, in the same order, with plan's context `research.notes` only, build and review taking `plan.spec`, instructions on research, plan, build and review, and every other step equal to the default's; `TestBuildAcceptsInstructions` and `TestPlanAcceptsInstructions` cover each kind's `instructions` alone and beside a profile and skills, and refuse a non-string.
`TestSpecPortTakesADocumentOrAnAcceptedSpec` checks that the spec input of build, review and rebase accepts a `document` and an `accepted-spec` and nothing else, that other ports keep one type, and that a code head on a spec port or a document on a head port is still refused with the usual error.

`defaults_test.go` covers the role mapping, `rebase` included; `EffectiveProfile` with an explicit profile winning, the role profile, none for a review overridden to the Guildmaster or a step type without a role, and an unmodified definition; and, as the legacy digest, `ApplyProfileDefaults` defaulting only declared role profiles, never a rebase step, an explicit profile winning, no default for a review overridden to the Guildmaster, an unmodified input and the default changing the digest.

`TestUnconfiguredStandardsBinding` covers parsing and exact bidirectional empty/configured standards binding for both subjects and both reviewers.
`internal/interp/TestUnconfiguredStandardsReviewExample` is red-first evidence for copied-example intake through persistence, human review, replay and delivery gating on both record backends.

`TestResearchAcceptsProfileAndInstructions` covers omitted and valid profiles, instructions beside a profile, a non-string instructions value, invalid profile types and names, and refusal of skills both alone and alongside a profile, through full-workflow and isolated-step validation.
The original broader research settings tests captured [red](../evidence/research-profile-2026-10-23-red.txt) and [green](../evidence/research-profile-2026-10-23-green.txt) runs before the scope was narrowed to profile only; those runs are historical evidence, not verification of skills refusal.

`authority_test.go` was written red first against stub bodies ([red run](../evidence/s10-approval-enforcement-2026-10-21-red.txt)): which kinds are guarded, each weakening and non-weakening edit of the shipped default, the floor on the shipped and example workflows and on edits below it, and admission and refusal for each basis.
The reserved-id case of `TestValidationRefusals` was written red first ([red run](../evidence/quality-judge-2026-10-23-red.txt)).
