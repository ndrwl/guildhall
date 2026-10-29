# Quality rubric

This rubric judges what a Director produces: a merged pull request on a Guildhall repository and the task record around it, from the spec and decisions to the review outcomes, validation results and timeline.
It is not a rubric for the Guildhall binary's own correctness, which CI and the Director safety checks own.
The [review standard](review.md)'s rules S1 to S11 stay the merge gates; this rubric is the scored and trend layer above them and changes none of their wording.
Every practice it adds is optional and switched in the [quality settings](configuration.md#quality-settings).

## Gates, scores and trends

Every criterion is one of three kinds:

- A **gate** passes or fails and blocks delivery; a failed gate is a rule-cited finding or a refused merge, never a score.
- A **score** is 0 to 3 against the anchors given, recorded with the evidence it rests on.
- A **trend** is a number recorded per task and read over the reference set and a rolling window of 20 merges, never as a per-task target.

Every criterion names who judges it:

- `script`: deterministic, with no model call, runnable in CI or by the [`guildhall-self-review`](../skills/guildhall-self-review/SKILL.md) skill.
- `model`: a fresh [judge session](#running-it), neither the builder nor the Director, quoting its evidence.
- `Guildmaster`: a sampled human audit.

Script facts outrank executed tests and CI, which outrank the model judge, which the Guildmaster audit calibrates.
A model score contradicted by a script fact is wrong.

## Strength

Each criterion names its source and how strong the evidence behind it is:

| Strength | Meaning |
|---|---|
| Strong | Peer-reviewed empirical studies at scale, replicated or corroborated across organisations, or a standard the whole industry enforces. |
| Moderate | One substantial empirical study, a large vendor dataset with a stated method, or a published model used in practice for years. |
| Practitioner | Guidance published by strong engineering organisations without a controlled study behind it, but adopted widely. |
| Weak | A single small or vendor-linked study, or an argument from first principles, included because it is the best available and the criterion is cheap to check. |

## Why this shape

Code review finds fewer defects than people expect; its main products are understanding and maintainability (Bacchelli and Bird 2013; Bosu, Greiler and Bird 2015).
Small changes are reviewed better: defects found per line fall as a change grows past a few hundred lines (SmartBear and Cisco 2006; Google's small-changes guidance).
Studies of AI-written code in 2025 found more duplication, less refactoring, more review findings and insecure choices in about 45% of tasks, so this rubric weights duplication, scope, insecure defaults and stability after merge above what a human-team rubric would.
Model judges agree with humans about 80% of the time but favour the first answer, longer answers and possibly their own, and miss real bugs when they judge code without running it (Zheng and colleagues 2023; Wang and colleagues 2025).
So scripts and executed tests decide facts, a model grades only what needs judgement and quotes its evidence, the Director never grades its own task, and a Guildmaster audit measures the judge's drift.
Every delivery metric can be gamed once it becomes a target, so trends are paired with a counter-metric and never become per-task targets (DORA 2023; SPACE 2021).

## Records

The criteria read these records:

- the pull request's body and diff;
- each `review-<n>-outcome`, with its findings and dispositions;
- each `<step>-<n>-inputs` and attempt result file, with the red and green runs;
- each `validate-<k>-result`, naming the checks of the exact head;
- `task timeline --json`, with attempts and `duration_seconds`, decisions and `wait_seconds`, findings, rulings and events;
- `director status --json`, with `needs_me` and the unread counts;
- the CI runs for the head and for `main` after the merge.

## Criteria

### Intent and scope (A)

**A1 Spec fidelity** (gate; model, Guildmaster at merge).

- The head implements every acceptance criterion of the accepted spec and nothing the spec did not ask for.
- Look at: `accept.spec` against the diff, and `spec-` findings in the review outcome.
- Good: each criterion maps to a test and a code change, with no unrequested behaviour.
- Poor: a criterion missing or partial, or behaviour the spec never named.
- Source: Google's code review guidance on functionality and design (Practitioner); ISO/IEC/IEEE 29148 verifiability (Strong).
- Guildhall today: S1 and the review skill's Spec axis.

**A2 Spec quality** (score; script for shape, model for substance).

- The accepted spec's acceptance criteria are singular, unambiguous and verifiable by reading the diff; out of scope and size are stated; each contract names errors, ownership, concurrency, idempotency, identity and recovery or says why not.
- Look at: the `plan.spec` document.
- Good (3): every criterion checkable on its own, and a size of one pull request or a stated split.
- Poor (0): user-story prose, "should work well", and no out of scope.
- Source: ISO/IEC/IEEE 29148 requirement characteristics (Strong); SWE-bench Verified's finding that under-specified task text made tasks unsolvable (Moderate).
- Guildhall today: the spec skill's completion rule; scoring it shows a weak spec even when the build later passes.

**A3 Behaviour and refactor separated** (score; script flags, model confirms).

- A pull request changes behaviour or restructures, not both, unless the restructure is a local rename.
- Look at: the diff's hunks and the commit list.
- Good: a pure refactor shows pinning tests only and no spec change, and a behaviour change touches what the spec names.
- Poor: a bug fix that also moves three files.
- Source: Google's small-changes guidance (Practitioner); GitClear's measured decline in refactoring (Moderate).
- Guildhall today: S6 and the contract-TDD rule that refactoring belongs to review.

**A4 Right questions to the Guildmaster** (score; model, Guildmaster audit).

- Decisions opened for the Guildmaster are ones the repository, the docs and the task could not settle, each carries a recommended answer, nothing is asked that a file answers, and no product choice is settled silently.
- Look at: the grill notes' settled points and questions, the timeline's `decisions` with `wait_seconds`, and whether the Guildmaster took the recommendation or corrected the premise.
- Good: few decisions, each with a recommendation the Guildmaster accepts or adjusts, and settled points that cite their sources.
- Poor: a decision asking what `docs/` already says, or a design choice made without one.
- Source: the grilling format Guildhall adopted (Practitioner); SPACE's collaboration dimension (Moderate).
- Guildhall today: the grill skills and [D61](decisions/0061-grill-live-interview.md); H2 is its trend.

### Correctness and tests (B)

**B1 Red-first evidence** (gate; script for presence, model for meaning).

- Every new behaviour has a test shown failing before the change and passing after, with command and exit code, and a test that passed the first time is listed as a pin.
- Look at: the build result file's evidence section and the pull request body.
- Good: the red output names the behaviour rather than a compile error, and the green run is the full suite.
- Poor: "tests pass" with no red run, a red that is a missing symbol, or a retrofitted test described as red then green.
- Source: Fucci and colleagues 2017 on the test-first cycle (Moderate); the red run as proof against a tautological test (Practitioner).
- Guildhall today: S2 and the contract-TDD evidence section.

**B2 Test quality** (score; model).

- Tests observe behaviour through the public contract, take expected values from the spec or a literal, name their cases, and fail with input, got and want, with no tautology, no unexported state and no double of the code's own collaborators.
- Look at: new and changed `*_test.go` files.
- Good (3): table tests with named cases and `f(in) = got, want` messages, and a shared contract suite where a double exists.
- Poor (0): assertions on private fields, an expected value computed the way the code computes it, or one giant test of many steps.
- Source: the Go wiki's TestComments and TableDrivenTests (Practitioner); Google's guidance on test sizes and hermetic tests (Practitioner).
- Guildhall today: the contract-TDD skill's `go-tests.md` and anti-patterns.

**B3 Determinism** (score and trend; script and model).

- No sleep is used as synchronisation, and nothing depends on the wall clock, test order or a shared temporary path; the suite passes twice with `-count=1` and under `-race` where supported.
- Look at: the diff for `time.Sleep`, `time.Now` in assertions, and fixed ports or paths; CI reruns of the same head; the hallmark's flake classification.
- Good: a new concurrency test waits on a channel or a condition, and the count of sleeps in tests does not rise.
- Poor: a retry wrapper around a flaky case, or a sleep tuned until CI passes.
- Source: Luo and colleagues 2014 on flaky-test causes (Strong); Google's flaky-test data from 2016 and 2017 (Strong).
- Guildhall today: S2 and the hallmark's sleep count; the facts script records the sleep delta per change.

**B4 Contract coverage** (score; model).

- The tests exercise each clause the contract names: every error and where it applies, ownership and concurrency, idempotent retry, identity, recovery and replay, and a double passes the same suite as the real adapter.
- Look at: the doc comments and the package's `docs/contracts/` page against the test names.
- Good: a case per clause, and a transition test such as a lost acknowledgement, a replay or a stale identity where the contract names one.
- Poor: the happy path only, or a double that re-implements the real adapter's rules.
- Source: Guildhall's [contract method](contracts.md) (project); Google's hermetic testing guidance (Practitioner); Parnas 1972 on information hiding (Strong).
- Guildhall today: S11, the contract method and the hallmark's seam map.

**B5 Test strength, not coverage as a target** (trend; script reports, model probes).

- Statement coverage of the changed packages is recorded, never targeted, and the model asks for each changed branch which test would fail if its condition were inverted.
- Look at: `go test -cover` on the changed packages, and the diff's conditionals.
- Good: every new branch has a test that would catch its inversion.
- Poor: high coverage from one broad test that asserts little.
- Source: Ivanković and colleagues 2019 and Petrović and colleagues 2021 at Google (Moderate).
- Guildhall today: nothing; it is new and report-only.

**B6 Suite budget** (trend; script).

- Suite wall time per platform and the slowest tests are recorded, and a change that adds more than 5 seconds to any package's `go test` time says why.
- Look at: `go test -json` timings and the CI test step durations.
- Good: a new integration test replaces a slower one or is justified.
- Poor: the Windows test step drifting up with no owner.
- Source: Fowler's Continuous Integration, revised in 2024 (Practitioner); Google's finding that larger tests are flakier (Strong).
- Guildhall today: the hallmark's test step; this records the delta per change instead.

### Design and maintainability (C)

**C1 Depth over abstraction** (gate for the forbidden cases, score otherwise; script flags, model judges).

- No interface is added for a pure function, a value type or a single implementation with no replacing test; new modules are deep, with much behaviour behind a small interface, and pass the deletion test.
- Look at: new `interface` declarations and their implementation count, and new packages and their exported surface.
- Good: a concrete type first, and an interface only where a real adapter and a double both exist.
- Poor: a plugin registry, an event bus, or an interface with one implementation kept for later.
- Source: Ousterhout's A Philosophy of Software Design (Practitioner); Parnas 1972 (Strong).
- Guildhall today: S6, S11 and the hallmark's vocabulary.

**C2 Duplication** (score and trend; script and model).

- No second copy of a validator, decoder, digest helper, atomic writer, name literal or platform branch appears, and no new block of five or more lines duplicates adjacent code.
- Look at: `dupl` output on the diff and the hallmark's list of usual duplicates.
- Good: the change calls the existing helper or moves it to the shared place.
- Poor: copy and paste with a renamed variable, or the same `switch` in two files.
- Source: GitClear 2025's eightfold rise in duplicated blocks (Moderate); the SIG maintainability model's duplication property (Moderate); Fowler's Duplicated Code smell (Practitioner).
- Guildhall today: the review skill's smells baseline and the hallmark's scan.

**C3 Complexity bounded** (score; script and model).

- New or changed functions stay under a cognitive-complexity threshold of 15 by `gocognit`, report-only, and new nesting is justified.
- Look at: `gocognit` or `gocyclo` on the changed functions.
- Good: a long function that is a flat table or sequence, with named branches.
- Poor: nested conditionals added to a function that is already complex.
- Source: the SIG model's unit complexity (Moderate); Tornhill and Borg's Code Red, finding about 15 times more defects in low-health code (Moderate); SonarSource's Cognitive Complexity (Practitioner).
- Guildhall today: nothing explicit; it is report-only until calibrated.

**C4 Locality and naming** (score; model).

- One concept lives in one place, names reveal intent, comments say why rather than what, and callers do not parse another module's names.
- Look at: the diff and the smells baseline.
- Good: a reader of the diff needs no second file to understand a name.
- Poor: possible Feature Envy or Shotgun Surgery at more than one site.
- Source: Google's guidance on naming and comments (Practitioner); Bacchelli and Bird on review value lying mostly in readability (Strong).
- Guildhall today: the review skill's `smells.md`.

**C5 Rework** (trend; script).

- The share of a task's merged lines changed again within the next 14 days or the next 5 merged pull requests is recorded, with whether a later pull request cites the task as its cause.
- Look at: `git log -p` over the window, and pull request bodies naming earlier tasks.
- Good: near zero for a feature; a fix for a bug the task introduced counts fully.
- Poor: half the diff rewritten by the next task.
- Source: GitClear's churn definition (Moderate); DORA 2024's rework rate (Moderate).
- Guildhall today: nothing; it is new and trend-only.

### Change hygiene and communication (D)

**D1 Size and shape** (score; script flags, model judges).

- Non-test lines changed and files touched are recorded, and above the soft budget the pull request says why it could not split.
- The budget is `quality_size_budget_lines` and `quality_size_budget_files` in the [quality settings](configuration.md#quality-settings), 400 non-test lines or 15 files by default; going over is flagged, never blocked.
- Look at: `git diff --stat`, or the facts script's `nontest_lines`, `files` and `size_over`.
- Good: under budget, or a stated expand-then-contract split.
- Poor: 1,200 lines across 40 files with no justification.
- Source: SmartBear and Cisco 2006 (Moderate); Google's small-changes guidance (Practitioner); Sadowski and colleagues 2018, a median of 24 changed lines at Google (Moderate).
- Guildhall today: S6, which keeps "no fixed line cap"; the budget is a flag beside it, not a cap.

**D2 What, why and evidence in the pull request text** (score; script for shape, model for substance).

- The title says what; the body says why, cites the spec and requirement ids, and lists what was checked with exit codes and what was not.
- Look at: the pull request body and the commit messages.
- Good (3): a reader can review from the body alone, and the evidence is reproducible.
- Poor (0): a title only, "tests pass", or claims with no command.
- Source: Tian and colleagues 2022 on good commit messages (Strong); Google's guidance on change descriptions (Practitioner).
- Guildhall today: `CONTRIBUTING.md`'s evidence rule and S4.

**D3 Review usefulness** (score and trend; script for structure, model and Guildmaster for substance).

- Each finding names a file, line, rule and concrete failure and is actionable, suggestions are listed apart from findings, dispositions of the previous round are correct, and no open (unresolved or disputed) finding survives two rounds without a Director ruling.
- Look at: each `review-<n>-outcome`, the rulings and the builder's response.
- Good: findings the builder fixed in one round, and few resolved without a fix.
- Poor: nitpicks without rules, a finding whose `file:line` is not in the diff, or a finding resolved without a fix that was wrong on its face.
- Source: Bosu, Greiler and Bird 2015 on useful review comments (Strong); Google's review standard (Practitioner).
- Guildhall today: the review skill and [D47](decisions/0047-review-skill.md).

**D4 Decisions recorded** (gate; script and model).

- Each engineering decision the pull request makes has a record with context, decision and consequences, as does each new dependency, CI action version or config key.
- Look at: additions under `docs/decisions/` against changes to `go.mod`, `.github/` and the config schema.
- Good: one record per decision, with consequences including the negative ones.
- Poor: a new module with no record, or a record that lists only benefits.
- Source: Nygard 2011 on architecture decision records; the ThoughtWorks Technology Radar's Adopt rating (Practitioner).
- Guildhall today: S7 and S10.

**D5 Docs truth** (gate for claims, score for completeness; script and model).

- Docs describe what is built, "not yet" and "later" claims match the code, Markdown has one sentence per line, and the README command list matches the help.
- Look at: the `docs/` diff and `cli.TestHelpNamesReadmeCommands`.
- Good: a new command has its page and README line in the same pull request.
- Poor: a document that describes the planned form as built.
- Source: DORA 2024 on documentation quality (Moderate); S4 and S10 (project).
- Guildhall today: S4, S10 and the hallmark's docs step.

### Delivery and stability (E)

**E1 Checks green on the exact head** (gate; script).

- Build, vet, format, tests and lint pass on all three runners for the merged head, and the validation result names that head.
- Look at: each `validate-<k>-result`.
- Source: Fowler's Continuous Integration; DORA (Strong).
- Guildhall today: S3 and the [validate](interpreter.md#validate) step.

**E2 Change failure** (trend; script).

- A merged task counts as a failure when `main` goes red at its merge, a revert follows, or a later pull request fixes a defect it introduced within 14 days.
- Look at: `main` CI runs after the merge, reverts, and fix pull requests citing the task.
- Good: under 10% over a rolling 20 merges, inside DORA's elite range of 0% to 15%.
- Poor: two red `main` runs in ten merges.
- Source: DORA's four key metrics (Strong).
- Guildhall today: the hallmark's CI classification.

**E3 Lead time and rounds** (trend; script).

- Time from `task new` to merge is recorded, split into attempt time, Guildmaster wait and checks wait, with review rounds, fix rounds and retries.
- Look at: the timeline's `duration_seconds`, `wait_seconds` and attempt count per step.
- Good: rounds trend down while D3 holds.
- Poor: a task at its maximum rounds with the same unresolved finding.
- Source: DORA lead time (Strong); Google's review-speed guidance (Practitioner); Goodhart's law as the warning against targeting it (Moderate).
- Guildhall today: the timeline already carries the numbers.

**E4 Main stays green** (gate for the Director's next action; script).

- A red `main` is the next task before new work starts.
- Look at: `main` CI after each merge, and the Director's next `task new`.
- Source: Fowler's rule that nobody has a higher priority task than fixing the build (Practitioner).
- Guildhall today: nothing makes the Director act on it.

**E5 Cost** (trend; script).

- Tokens and cost per task, per attempt and per merged non-test line are recorded, with the count of model invocations while the task waited, which must be zero.
- Look at: each attempt's recorded [spend](director.md#spend) and the Director safety checks' idle-call check.
- Good: cost per merged line flat or falling as rounds fall.
- Poor: a fix loop that doubles the cost without changing the finding.
- Source: SPACE's efficiency dimension (Moderate); R1.14 and R1.61 (project).
- Guildhall today: `director status` and the timeline report spend as measured, estimated or unknown.

### Security and supply chain (F)

**F1 No secrets or private content** (gate; script and model).

- No credentials, tokens, machine names, private paths or personal preferences appear in code, tests, docs, commit messages or pull request text.
- Look at: the diff and pull request body, and the facts script's `secret` items.
- Source: OWASP and OpenSSF consensus (Strong).
- Guildhall today: S5, R1.2 and R1.10.

**F2 Workflow least privilege and pinning** (gate; script).

- Workflows declare read-only permissions at the top and write per job, actions are pinned by commit SHA, no `pull_request_target` checks out untrusted code, and releases come only from a pushed tag with every artifact in `SHA256SUMS`.
- Look at: the `.github/` diff, and the facts script's `write-permission`, `unpinned-action` and `pull-request-target` items.
- Source: the OpenSSF Scorecard's Token-Permissions, Pinned-Dependencies and Dangerous-Workflow checks (Practitioner standard, Strong adoption).
- Guildhall today: S9; actions are still pinned by tag.

**F3 Dependency discipline** (gate; script and model).

- The standard library comes first, each new module or tool version has a decision and a reason, and `go mod tidy -diff` and `go mod verify` are clean.
- Source: the Software Engineering at Google book's chapter on dependency management (Practitioner); OpenSSF (Strong).
- Guildhall today: S7 and D3.

**F4 Insecure defaults in generated code** (score; script and model).

- Inputs at process, file, shell and forge boundaries are validated, no shell string is interpolated, file modes are explicit, temporary files are created safely, and errors do not leak paths or secrets into messages that reach a forge.
- Look at: `gosec` and `govulncheck` on the diff, and every boundary crossing read by the model.
- Good: `os/exec` argument lists, per-shell quoting with refusal, and `0o600` where a file is private.
- Poor: a command built from a string, or a path joined from user text without checks.
- Source: Veracode 2025, insecure choices in about 45% of tasks (Moderate); the OWASP Top 10 (Strong).
- Guildhall today: the [platform and shell contract](contracts.md#platforms-and-shells).

### Honesty and evidence (G)

**G1 Claims match evidence** (gate; model, Guildmaster audit).

- Nothing is described as supported, tested or verified beyond what the recorded runs show, a check that could not run is missing evidence rather than a pass, and platform claims follow R1.27 and R1.30.
- Look at: the pull request body and result files against the checks listed.
- Good: "not run on macOS; CI only".
- Poor: "works on Windows" with no Windows run.
- Source: Google's rule that technical facts outrank opinions (Practitioner); METR 2025's gap between felt and measured speed (Moderate).
- Guildhall today: S4 and the hallmark's rule on missing evidence.

**G2 Tests never weakened** (gate; script flags, model confirms).

- Any deleted or loosened assertion, removed case or widened tolerance cites the requirement or decision that makes the old expectation wrong.
- Look at: `*_test.go` deletions and changed `want` values, and the facts script's `assertion-removed` items.
- The facts script flags only removed `Error`, `Errorf`, `Fatal`, `Fatalf`, `Fail` and `FailNow` calls on a `t`, `b`, `tb` or `f` testing receiver, so a model always confirms G2.
- Source: the [contract method](contracts.md) (project); consensus.
- Guildhall today: the contract-TDD rule.

**G3 Skips, sleeps and platform files declared** (score; script).

- New skips use `testenv.SkipUnlessRequired` or are justified, and new sleeps and platform-only files are listed in the pull request.
- Look at: the facts script's `skips_added`, `sleeps_added` and their items.
- Source: [D46](decisions/0046-ci-required-skips.md) (project); Google's flaky-test data (Strong).
- Guildhall today: D46 and the hallmark's counts.

**G4 Judge calibration** (trend; Guildmaster).

- For a sample of delivered tasks the Guildmaster answers three questions in under two minutes: would you have merged this as is, was anything asked that should not have been, and was anything decided that should have been asked.
- Agreement between the model judge's scores and these answers is tracked, with the judge's consistency when the order of a pair is swapped.
- The sample is one in `quality_audit_sample` delivered tasks, five by default, plus every reference run.
- Source: Zheng and colleagues 2023 (Strong); Wang and colleagues 2025 on code judges (Moderate).
- Guildhall today: independently of `quality_self_review`, deliver opens the `audit` decision, bound to the head that merged, for each sampled task as it closes ([interpreter](interpreter.md#publish-and-deliver)); the answer's first line is `yes` or `no` to the first question and the note holds the other two; `guildhall quality agreement` counts how often the judge's yes or no to the same question matches it, overall and by whether the judge's family was separate; position-swap consistency is not yet computed, because only reference evaluations compare pairs, and reference runs are not yet sampled because they do not exist yet; it is the only criterion that needs the Guildmaster on a schedule.

### Process efficiency and Director judgement (H)

**H1 Rounds to approval** (trend; script).

- Review rounds, fix rounds and validation rounds per task are recorded, with the findings count per round.
- Good: one review round, with findings resolved in one fix.
- Poor: three rounds with a growing findings list.
- Source: DORA lead time (Strong); Bosu and colleagues on usefulness (Strong) as its paired quality metric.
- Guildhall today: the timeline.

**H2 Guildmaster interruptions** (trend; script and Guildmaster).

- Decisions per task are recorded, with the share answered with the recommendation, the share the Guildmaster marked unnecessary in the G4 audit, and the wait per decision.
- Source: SPACE's collaboration dimension (Moderate); keeping the Guildmaster in charge without babysitting (project).
- Guildhall today: the timeline's `wait_seconds`.

**H3 Recovery handled** (gate; script).

- Failed or stopped attempts were retried with a reason or escalated, nothing was launched twice, a stalled worker was read and acted on, and `needs_me` was drained before new work after a restart.
- Look at: `needs_me` history, `step retry` receipts with reasons, and the Director safety checks.
- Source: R1.53 and R1.61 (project).
- Guildhall today: the Director safety checks in CI.

**H4 Zero idle spend** (gate; script).

- No model is invoked while a task waits on the Guildmaster or on checks.
- Source: R1.14 and R1.61 (project).
- Guildhall today: the Director safety checks.

## Scoring and aggregation

- The gates are A1, B1, C1 for its forbidden cases, D4, D5 for its claims, E1, E4, F1 to F3, G1, G2, H3 and H4, and they already gate through review and validation.
- Scores are reported per pillar with no single total, and the lowest pillar leads the self-review summary.
- Each trend is paired with a counter-metric: H1 with D3, E3 with E2, E5 with C5, and H2 with A4.

## How S1 to S11 map

| Rule | Criteria |
|---|---|
| S1 Specification | A1 |
| S2 Tests first | B1, B3 |
| S3 Three platforms | E1 |
| S4 Honest claims | G1 |
| S5 Public safety | F1 |
| S6 Small and specific | C1, D1 |
| S7 Dependencies | F3, D4 |
| S8 Custody | H3 |
| S9 Release integrity | F2 |
| S10 Documentation | D4, D5 |
| S11 Contracts | B4, C1 |

## Running it

The Director runs the [`guildhall-self-review`](../skills/guildhall-self-review/SKILL.md) skill after a task merges, when `quality_self_review` is `on`; it is `off` by default.
The skill writes one `self-review-<n>.json` document per task under the copy's `quality/<repo>/<task>/` directory, with the facts its script prints, the gates and trends the records show, and each model-judged score left pending for a judge session that is neither the Director nor the builder.
`guildhall quality judge --repo <id> --review <n> <task>` launches that judge as an owned attempt of the reserved step `quality-judge` on the delivered task, under the guard rails and the pause hold, with the self-review, its brief, diff and facts beside its prompt ([decision](decisions/quality-judge.md)).
The judge requires a listed profile distinct from the builder's; an unset `quality_judge_profiles` or no eligible profile refuses the judge with `ErrNoJudge` and a reason.
The builder's profile comes from the latest succeeded build's prompt manifest, ordered by finish receipts.
The manifest does not record the adapter that executed the build, so the builder's family is unknown rather than inferred from today's profile settings; a judge of an unknown builder family is never counted as separate-family.
A judge profile currently selects its adapter and role only; its model and effort are not passed to the runtime by the judge's `LaunchFiles` path.
Ordinary workflow Artisan launches through `Process.Launch` do pass their profile's model and effort.
The judge runs as the first eligible profile in `quality_judge_profiles` when the builder's family is unknown; otherwise it prefers another family while `quality_judge_separate_family` is `on`.
A profile whose judge of the same self-review failed is passed over, so running it again after a judge ran out of quota falls back to the next; every choice records whether the family was separate and why not.
For interrupted judges, unlaunched preparations and attempts awaiting intake, follow the recovery commands in [Director status](director.md#status).
When the judge's attempt ends, its launcher runs `guildhall quality judged`, which records an accepted `judgement.json` as the task document `quality-judge-<n>-judgement` and tells the Director with a `judgement` message, or tells it `judge-failed` or `judgement-refused` and records nothing; the task stays done.
The skill then copies the judge's scores into the self-review with `judge: model`.
The self-review is for learning and for evaluating workflow changes; it never re-gates a merged task.
Independently of self-review, deliver opens the Guildmaster's `audit` decision for one in `quality_audit_sample` delivered tasks, chosen by the task's identity; the default is 5, 0 disables audits, and 1 audits every task ([decision](decisions/0087-quality-audit.md)).
A task created with `guildhall task new --fixes <repository>/<task>` carries "Fixes a defect introduced by task `<repository>/<task>`." in its pull request body, the line rework (C5) and change failure (E2) count from.
The lesson-proposal shape that applies `quality_skill_edits_need_approval`, the reference task set and its runner, and the rule for evaluating Director-proposed workflow changes are later work; their settings are already parsed and validated so a copy can set them now.

## Sources

- Google engineering practices: [What to look for in a code review](https://google.github.io/eng-practices/review/reviewer/looking-for.html), [The standard of code review](https://google.github.io/eng-practices/review/reviewer/standard.html), [Small CLs](https://google.github.io/eng-practices/review/developer/small-cls.html); Practitioner.
- Sadowski, Söderberg, Church, Sipko and Bacchelli, "Modern Code Review: A Case Study at Google", ICSE-SEIP 2018, [research.google](https://research.google/pubs/modern-code-review-a-case-study-at-google/); Moderate.
- Bacchelli and Bird, "Expectations, Outcomes, and Challenges of Modern Code Review", ICSE 2013, [Microsoft Research](https://www.microsoft.com/en-us/research/?p=164195); Strong.
- Bosu, Greiler and Bird, "Characteristics of Useful Code Reviews: An Empirical Study at Microsoft", MSR 2015, [PDF](https://microsoft.com/en-us/research/wp-content/uploads/2015/05/PID3556473.pdf); Strong.
- Cohen, SmartBear and Cisco code review study, 2006, summarised in [Best Practices for Code Review](https://smartbear.com/learn/code-review/best-practices-for-peer-code-review/); Moderate.
- Tian and colleagues, "What Makes a Good Commit Message?", ICSE 2022, [arXiv 2202.02974](https://arxiv.org/abs/2202.02974); Strong.
- Luo, Hariri, Eloussi and Marinov, "An Empirical Analysis of Flaky Tests", FSE 2014, [FSE 2024 test-of-time note](https://2024.esec-fse.org/details/fse-2024-plenary-events/10/An-empirical-analysis-of-flaky-tests); Strong.
- Micco, "Flaky Tests at Google and How We Mitigate Them", 2016, and "Where do our flaky tests come from?", 2017, [Google Testing Blog](https://testing.googleblog.com/2016/05/); Strong.
- Fucci and colleagues, "A Dissection of the Test-Driven Development Process", IEEE TSE 2017, [arXiv 1611.05994](https://arxiv.org/abs/1611.05994); Moderate.
- Ivanković, Petrović, Just and Fraser, "Code Coverage at Google", ESEC/FSE 2019, and Petrović, Ivanković, Fraser and Just, "Does mutation testing improve testing practices?", ICSE 2021, [abstracts](https://homes.cs.washington.edu/~rjust/publ/); Moderate.
- Heitlager, Kuipers and Visser, "A Practical Model for Measuring Maintainability", QUATIC 2007, [PDF](https://www.di.uminho.pt/~joost/publications/HeitlagerKuipersVisser-Quatic2007.pdf); Moderate.
- Tornhill and Borg, "Code Red: The Business Impact of Code Quality", TechDebt 2022, [arXiv 2203.04374](https://arxiv.org/abs/2203.04374); Moderate.
- Campbell, "Cognitive Complexity", SonarSource, [sonarsource.com](https://www.sonarsource.com/resources/cognitive-complexity/); Practitioner.
- Ousterhout, A Philosophy of Software Design, 2018; Practitioner.
- Parnas, "On the Criteria To Be Used in Decomposing Systems into Modules", CACM 1972; Strong.
- Fowler, "Continuous Integration", revised January 2024, [martinfowler.com](https://martinfowler.com/articles/continuousIntegration.html); Practitioner.
- DORA, "Accelerate State of DevOps Report 2024", [dora.dev](https://dora.dev/research/2024/dora-report/); Strong for the four key metrics, Moderate for the AI findings.
- DORA, "State of AI-assisted Software Development 2025", [PDF](https://services.google.com/fh/files/misc/2025_state_of_ai_assisted_software_development.pdf); Moderate.
- Forsgren, Storey, Maddila, Zimmermann, Houck and Butler, "The SPACE of Developer Productivity", ACM Queue 2021, [queue.acm.org](https://queue.acm.org/detail.cfm?id=3454124); Moderate.
- GitClear, "AI Copilot Code Quality: 2025 Data", 211 million changed lines from 2020 to 2024, covered by [LeadDev](https://leaddev.com/software-quality/how-ai-generated-code-accelerates-technical-debt); Moderate.
- CodeRabbit, "State of AI vs Human Code Generation", December 2025, [coderabbit.ai](https://coderabbit.ai/blog/state-of-ai-vs-human-code-generation-report); Moderate.
- Veracode, "2025 GenAI Code Security Report", [press release](https://www.veracode.com/press-release/ai-generated-code-poses-major-security-risks-in-nearly-half-of-all-development-tasks-veracode-research-reveals/); Moderate.
- METR, "Measuring the Impact of Early-2025 AI on Experienced Open-Source Developer Productivity", July 2025, [metr.org](https://metr.org/blog/2025-07-10-early-2025-ai-experienced-os-dev-study/); Moderate.
- Zheng and colleagues, "Judging LLM-as-a-Judge with MT-Bench and Chatbot Arena", NeurIPS 2023, [arXiv 2306.05685](https://arxiv.org/abs/2306.05685); Strong.
- Wang and colleagues, "On the Effectiveness of LLM-as-a-judge for Code Generation and Summarization", 2025, [arXiv 2507.16587](https://arxiv.org/abs/2507.16587); CodeJudgeBench, [arXiv 2507.10535](https://arxiv.org/abs/2507.10535); Moderate.
- OpenAI, "Introducing SWE-bench Verified", August 2024, [openai.com](https://openai.com/index/introducing-swe-bench-verified/); Moderate.
- OpenSSF Scorecard checks, [checks.md](https://github.com/ossf/scorecard/blob/main/docs/checks.md); Practitioner standard, Strong adoption.
- Nygard, "Documenting Architecture Decisions", 2011, and the ThoughtWorks Technology Radar's [Lightweight Architecture Decision Records](https://www.thoughtworks.com/radar/techniques/lightweight-architecture-decision-records); Practitioner.
- ISO/IEC/IEEE 29148:2018, requirement characteristics; Strong.
- The Go project wiki's [TestComments](https://go.dev/wiki/TestComments) and [TableDrivenTests](https://go.dev/wiki/TableDrivenTests); Practitioner.
