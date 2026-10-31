# Engineering decisions

Engineering decisions, one short record each, written by the pull request that makes them.
Product requirements live in `docs/requirements.md`.
A store "decision" (a Guildmaster question, `guildhall decide`) is something else: see [the glossary](glossary.md#decision).

Read the index below to choose what to open: each line is the decision as a statement, grouped by topic.
Superseded records are listed apart, and the Former names table resolves every old number (`D1` to `D96`) and every old, merged or moved file name.

## Convention

### Identity

A record is `docs/decisions/<slug>.md` and keeps that name for life.
The slug matches `^[a-z0-9]+(-[a-z0-9]+)*\.md$`, so it carries no number, and no new numbers are assigned.
Cite a record with a Markdown link or its path, never a bare `Dn`.
The rubric's criteria D1 to D5 (`docs/quality.md`) and task ids `d<n>` are unrelated to the old numbers.

### Header

A record starts with front matter of simple `key: value` lines between `---` lines:

```text
---
status: accepted
date: 2026-10-30
topic: delivery
tasks: d60, d87
supersedes: an-older-record.md
amends: another-record.md
superseded-by:
amended-by:
formerly: D60, 0060-delivery-stage.md
---
```

- `status` is `accepted`, `designed` (decided, not built), `superseded` or `rejected`.
- `date` is the day the decision was accepted, as `YYYY-MM-DD`; a compacted record keeps the latest.
- `topic` is one of the topics `internal/doccheck` lists, which are the index's headings.
- `tasks` names the Guildhall tasks whose working papers hold the detail.
- `supersedes`, `amends`, `superseded-by` and `amended-by` hold comma-separated record file names, and each pair names both records.
  A record with `superseded-by` has status `superseded`.
- `formerly` lists the record's old numbers and old file names, including those of records merged into it.
  No `formerly` entry is ever removed.

Fields with no value may be left out.

### Body

```text
# <The decision, as a statement>

## Context
Why this came up: the constraint or incident, with task ids.

## Decision
What was chosen, in a few sentences, linking the contract or topic page for behaviour detail.

## Alternatives rejected
- <option>: <why not>

## Consequences
What this costs and constrains, including the negative.

## History            (compacted records only)
- <date>, <former name>: <what changed and why; alternatives rejected then>
```

`## Context`, `## Decision` and `## Consequences` are required; `## Alternatives rejected` is included when there were real alternatives.
Aim for 40 lines or fewer; review checks that aim, no test does.
Behaviour detail belongs on the contract or topic page the record links, not copied into the record.

### The bar

Write a record when the pull request:

1. chooses between real alternatives that someone would otherwise reopen;
2. constrains future work: a boundary, an invariant, a format, a stored or wire schema, or a command grammar;
3. adds a dependency, CI action, tool version or config key;
4. reverses or amends a record; or
5. makes a deliberate non-goal or limit, with its reason.

Do not write a record for:

1. a task's plan, slice steps or delivery order, which stay in the task's working papers in the store;
2. a bug fix with no design choice;
3. a test budget or flake fix, which goes to [test timing](test-timing.md);
4. behaviour reference, which goes to the contract or topic page;
5. a review or report, which goes to `docs/evidence/` or stays in the store; or
6. a decision that is already recorded: amend or supersede it instead.

### Changing a decision

A pull request may change an existing record's header (`status`, `superseded-by` and `amended-by`) or fix a link in it.
Any other change is a new record that names what it supersedes or amends, and the same pull request marks the old record.

### Compaction

A periodic task, picked by the Guildmaster from a hallmark follow-up, merges a chain into one current record.
A chain qualifies at three or more records, or when a superseded or amended record has stood longer than one hallmark cycle.
The current record's `## History` keeps each absorbed record's date, what changed, why, and the alternatives rejected then.
The absorbed names go into its `formerly`, and the absorbed files are deleted.

### This index

Everything below the marker line is generated from the records' headers.
Regenerate it with `go test ./internal/doccheck -run TestDecisionIndex -update`, including to resolve a merge conflict in it.
`go test ./internal/doccheck` also checks every header, every supersede and amend pair, and every link into `docs/decisions/` from Go, Markdown and skills.

<!-- Generated from the records' headers below this line: do not edit by hand. -->

## Foundation and working method

- [Guildhall is developed contract first, with packages organised by domain](decisions/contract-first-development-and-package-layout.md)
- [`doctor` reports only what it checked, and a missing tool is not a failure](decisions/doctor-reports-only-what-it-checked.md)
- [The module targets Go 1.27](decisions/go-1-27.md)
- [The repository is one Go module that builds one binary](decisions/one-go-module-and-one-binary.md)
- [One strict JSON decoder serves Guildhall's JSON formats](decisions/shared-strict-json.md)

## Dependencies

- [The store uses bbolt, with JSON records and no generic storage interface](decisions/bbolt-for-the-store.md)
- [The task pages render Markdown documents with goldmark and its tables extension at safe defaults](decisions/goldmark-for-documents.md)
- [The foundation uses only the standard library, and dependencies arrive with the feature that needs them](decisions/standard-library-only-for-the-foundation.md)
- [Copy configuration is parsed with BurntSushi/toml](decisions/toml-parsing-for-copy-configuration.md)

## Continuous integration

- [CI pins actions by commit, runs a Linux race step and runs the release test](decisions/ci-action-pins-and-race.md)
- [CI builds, vets, tests and lints on Linux, Windows and macOS with a pinned golangci-lint](decisions/ci-matrix-and-lint.md)
- [A CI runner's required tools turn their skips into failures](decisions/ci-required-skips.md)
- [Guildhall fetches failed CI job logs for Artisans, who get no `gh`](decisions/failed-job-logs.md)
- [Tests skip the store's fsync, scale wait ceilings on CI and share fixtures, to keep Windows CI fast and steady](decisions/windows-ci-speed.md)

## Releases and the baseline

- [Role prompts and stage guides ship with the binary](decisions/release-baseline.md)
- [Releases are five static cross-compiled binaries with checksums, published only from a version tag](decisions/release-builds.md)
- [The release policy test parses the workflow with Python and PyYAML, as development-only dependencies](decisions/release-policy-regression-test.md)
- [Release skills live in the top-level `skills/` directory and are embedded in the binary](decisions/release-skills-root.md)
- [Releases install side by side, a per-copy pin selects one, and tasks keep the release they started on](decisions/release-upgrades.md) (designed, not built)
- [`guildhall version` reads its revision from Go build info, and the Director chat shows it first](decisions/version-from-build-info.md)

## Store

- [Inspection reads take their times from store receipts](decisions/inspection-reads-from-store-receipts.md)
- [The store's single-writer rule uses bbolt's own file lock](decisions/store-single-writer-lock.md)
- [A typed decision is bound to the artefact its answer applies to, with option-checked answers](decisions/typed-decisions-bound-to-an-artefact.md)

## Command line

- [One command grammar for the Director, Artisan and Steward roles](decisions/cli-role-grammar.md)
- [A command finds its copy by `--copy` or by a copy marker above the current directory, and no environment variable selects a copy](decisions/copy-discovery.md)

## Workflows and their binding

- [Who fulfils a step is an actor setting, not part of the step kind's name](decisions/actor-as-a-step-setting.md)
- [Rails, not tracks: the engine enforces delivery invariants, the workflow supplies the normal route, and the Director chooses a safe route when circumstances change](decisions/rails-not-tracks.md)
- [An opt-in refute step attacks the plan before acceptance, as advisory findings](decisions/refute-step.md)
- [A report-only workflow finishes as done without delivering, and the hallmark task kind runs one](decisions/report-only-workflows.md)
- [Retired shipped workflow revisions stay resolvable for the tasks that bound them](decisions/retired-shipped-revisions.md)
- [Tasks resolve their bound revision by name, and role profiles are resolved at use, outside the digest, with Guildhall shipping them](decisions/role-profiles-outside-the-digest.md)
- [A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance](decisions/task-kinds.md)
- [Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow](decisions/workflow-approvals.md)

## Engine, launcher and watcher

- [Each attempt records the tokens and cost its runtime reports, marked measured, estimated or unknown](decisions/attempt-spend.md)
- [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md)
- [A failed or stopped attempt has a next attempt, started by step retry](decisions/failed-attempt-retry.md)
- [An attempt's prepared files are published once and never replaced](decisions/immutable-attempt-preparation.md)
- [Rejected result intake is recorded separately from launcher completion](decisions/intake-rejection.md)
- [`internal/launch` is the only place the owned attempt commands are composed](decisions/launch-composition.md)
- [Loops and the review gate are derived from recorded attempt bindings](decisions/loops-and-review-gate.md)
- [Attempt prompts and skills are rendered once into self-contained files with platform-independent digests](decisions/rendered-prompts-skills-and-digests.md)
- [The interpreter owns its launch seam, and one scripted mock agent stands in for workers in tests](decisions/scripted-mock-agent-and-launch-seam.md)
- [Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat](decisions/stall-observation.md)
- [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md)
- [The spend report shows recorded spend without inventing attribution](decisions/token-attribution-report.md)

## Containment and attempt ownership

- [The launcher owns each worker through one containment interface with a backend per platform](decisions/artisan-containment.md)
- [Artisan resume sweeps a recorded unit only after the owner-gone rule](decisions/artisan-resume.md)
- [An owned Artisan's timeout starts after setup, and a launcher that never started a worker records `not-started` evidence](decisions/artisan-timeout-and-not-started-evidence.md)
- [Containment persists as a recovery description, not a name](decisions/containment-persistence.md)
- [The store owns "the first finish stands"](decisions/finish-first-stands.md)
- [Linux containment uses a delegated cgroup v2, else a subreaper and process group](decisions/linux-cgroup-containment.md)
- [The launcher decides each attempt once, from its files before its processes](decisions/stop-ladder-precedence.md)
- [Attempt ownership lives in the store](decisions/store-attempt-ownership.md)

## Attempt endpoint

- [Each attempt's control endpoint is one AF_UNIX socket, on Windows as on Unix](decisions/attempt-endpoint-af-unix.md)
- [The attempt control protocol is versioned and decoded strictly](decisions/endpoint-protocol-v1.md)

## Runtimes and adapters

- [The Claude adapter runs print mode under a wrapper that owns the result](decisions/claude-adapter-wrapper.md)
- [The fake Artisan is a subcommand of the guildhall binary](decisions/fake-adapter-as-a-subcommand.md)
- [A profile's model and effort reach its runtime by value, effort falls back to a Guildhall setting, and a runtime refuses what it cannot honour](decisions/profile-model-effort.md)
- [One package owns the runtime names](decisions/runtime-adapter-owner.md)
- [An attempt's version 1 result file is published by hard link](decisions/version-1-result-file.md)

## Grants, profiles and permissions

- [Role-specific Claude skill defaults are resolved from Guildhall settings before rendering](decisions/claude-skill-defaults.md)
- [Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant](decisions/task-grants.md)

## Herdr and presentation

- [Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished](decisions/artisan-herdr-workspaces.md)
- [Clerk ships with the Director as its own embedded plugin and echoes fixed output](decisions/clerk-install-and-output.md)
- [Every Herdr call names its session explicitly, and Herdr 0.9.5 is the version floor](decisions/explicit-herdr-session-and-version-floor.md)
- [Guildhall drives Herdr through its command-line API](decisions/herdr-through-its-command-line-api.md)
- [Guildhall types the launch command into a new Herdr tab, quoted for the pane's shell](decisions/herdr-typed-launch-command.md)

## Artisans

- [Generate required output contracts with every Artisan prompt](decisions/artisan-output-instructions.md)
- [Build and fix stages use the guildhall-contract-tdd skill, adapted from upstream tdd](decisions/contract-tdd-skill.md)

## Review

- [Director rulings are store records, and a contested blocker opens the loop exit early](decisions/director-rulings.md)
- [Full-review requests are store records consumed by a round](decisions/full-review-request.md)
- [Rebase classification runs the git program through an internal/git boundary](decisions/rebase-classification.md)
- [A review outcome is guildhall.review-outcome/2, and a changes_requested verdict always loops back](decisions/review-outcome-2.md)
- [The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs](decisions/review-skill.md)

## Quality

- [The quality rubric ships as a document and a Director skill, each practice behind a setting, with model scores from a separate judge attempt](decisions/quality-rubric.md)
- [No close opens the Guildmaster's audit of a delivered task, and recorded audits stay answerable](decisions/retire-quality-audit.md)

## Delivery, merging and guard rails

- [Every Artisan attempt starts on the current main, and branch-kept replaces dependency-branch-kept](decisions/branch-kept.md)
- [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md)
- [Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off](decisions/guard-rails.md)
- [Authorized pull requests wait in a merge queue, and Guildhall rebases the front itself in three tiers](decisions/merge-queue-rebase.md)
- [Large work is one multi-PR task: an accepted spec lists ordered slices, delivered one pull request each](decisions/multi-pr-tasks.md)
- [Slices stay sequential: Guildhall does not stack their pull requests until merge-async and GitHub-made heads are designed for](decisions/stacked-prs-not-yet.md)
- [A task's working directory is a git worktree of its repository](decisions/worktree-custody.md)

## Tasks and holds

- [A closed task never has an open decision, and a hallmark stays open until its follow-ups are answered](decisions/closed-tasks-have-no-open-decisions.md)
- [A parked attempt, a deferred decision or a follow-up can be held until named tasks merge](decisions/hold-until-merged.md)
- [The Director parks a failed or stopped attempt with a reason, so it leaves needs_me](decisions/park-attempt.md)
- [A task can be cancelled for good, with a reason, voiding its decisions and closing its pull requests](decisions/task-cancel.md)
- [A task's dependencies change after creation through append-only records, folded into one effective list](decisions/task-dependency-changes.md)
- [A task override gives one task its own value for a task-scoped setting, on the Guildmaster's word](decisions/task-overrides.md)

## Director

- [Each Director backs up its own copy, as a store export and its copy files, to its own branch of a private repository](decisions/director-backups.md)
- [The session supervisor watches pending checks, and resume wakes the Director once](decisions/director-checks-watch.md)
- [Plain English to the Director is the Guildmaster's primary interface, and the commands stay complete and public](decisions/director-conversation-first.md)
- [Three triggers tell the Director a handoff is due, and Claude's context is read from the transcript by the core turn-end hook](decisions/director-handoff-triggers.md)
- [Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped](decisions/director-pause-stop.md)
- [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md)
- [The Director's runtime runs in a containment unit recorded on its session before its program can run](decisions/director-runtime-record.md)
- [The Director safety checks drive the built binary with stand-in runtimes](decisions/director-safety-checks.md)
- [The release ships five Director skills split by moment, and tests hold them to the README's commands](decisions/director-skills.md)
- [Claude Director sessions run in one stable workspace that the Guildmaster approves once in Claude's own trust dialog](decisions/director-stable-claude-workspace.md)
- [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md)
- [The Director's status feed is one store read plus files, with `needs_me` in a fixed order](decisions/director-status-feed.md)
- [`director start --tab` types the supervisor into one Director tab it owns, and replaces only shells it can prove it created](decisions/director-tab.md)
- [One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input](decisions/director-wake-hook.md)
- [The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes](decisions/grill-live-interview.md)
- [The Director addresses the user as "Guildmaster" in chat replies, and nowhere else](decisions/guildmaster-address.md)
- [Lanterns is a visuals-only plugin in every Claude Director workspace, with the S2 smithy and Calm on by default](decisions/lanterns-visuals.md)
- [The status feed and startup list every running attempt as information, without a new probe or `needs_me` kind](decisions/running-attempts-in-status.md)
- [The Guildmaster's task pages are served by the Director's session supervisor, on the tailnet only, from Clerk's readers](decisions/task-page.md)

## Messaging

- [The Guildmaster and other Directors send a Director typed messages over SSH, which inform but never start work](decisions/director-messaging.md) (designed, not built)
- [Director and Artisan messages are records in the Director's store](decisions/local-messaging-in-the-store.md)
- [One registry in the store lists the message types Guildhall sends, and the engine refuses any other](decisions/message-type-registry.md)

## Maintenance and hallmarks

- [Director maintenance is one skill with a note audit and a hallmark, each due on a counted cadence and recorded](decisions/director-maintenance.md)
- [A repository's last hallmark is its last done hallmark task, chosen by one engine selector](decisions/engine-owned-hallmark-cadence.md)

## Steward

- [Rollouts write only `managed.toml`, separate from the Director-owned `config.toml`](decisions/managed-publication-separate-from-director-config.md)
- [A Director runs standalone or managed, and no Director feature requires a Steward](decisions/optional-steward.md) (designed, not built)
- [Repository paths are absolute in the owner machine's form](decisions/repository-paths-are-absolute.md)
- [Rollout plans, previews and receipts live in one plan directory](decisions/rollout-plans-previews-and-receipts.md)
- [Steward rollouts copy files with scp and run no remote command](decisions/steward-rollouts-copy-with-scp.md)

## Former names

| Former name | Now in |
|---|---|
| D1 | [The repository is one Go module that builds one binary](decisions/one-go-module-and-one-binary.md) |
| D2 | [The module targets Go 1.27](decisions/go-1-27.md) |
| D3 | [The foundation uses only the standard library, and dependencies arrive with the feature that needs them](decisions/standard-library-only-for-the-foundation.md) |
| D4 | [CI builds, vets, tests and lints on Linux, Windows and macOS with a pinned golangci-lint](decisions/ci-matrix-and-lint.md) |
| D5 | [Releases are five static cross-compiled binaries with checksums, published only from a version tag](decisions/release-builds.md) |
| D6 | [`doctor` reports only what it checked, and a missing tool is not a failure](decisions/doctor-reports-only-what-it-checked.md) |
| D7 | [The release policy test parses the workflow with Python and PyYAML, as development-only dependencies](decisions/release-policy-regression-test.md) |
| D8 | [Copy configuration is parsed with BurntSushi/toml](decisions/toml-parsing-for-copy-configuration.md) |
| D9 | [The store uses bbolt, with JSON records and no generic storage interface](decisions/bbolt-for-the-store.md) |
| D10 | [The store's single-writer rule uses bbolt's own file lock](decisions/store-single-writer-lock.md) |
| D11 | [Repository paths are absolute in the owner machine's form](decisions/repository-paths-are-absolute.md) |
| D12 | [Steward rollouts copy files with scp and run no remote command](decisions/steward-rollouts-copy-with-scp.md) |
| D13 | [Rollout plans, previews and receipts live in one plan directory](decisions/rollout-plans-previews-and-receipts.md) |
| D14 | [Rollouts write only `managed.toml`, separate from the Director-owned `config.toml`](decisions/managed-publication-separate-from-director-config.md) |
| D15 | [Director and Artisan messages are records in the Director's store](decisions/local-messaging-in-the-store.md) |
| D16 | [Guildhall is developed contract first, with packages organised by domain](decisions/contract-first-development-and-package-layout.md) |
| D17 | [Guildhall drives Herdr through its command-line API](decisions/herdr-through-its-command-line-api.md) |
| D18 | [Every Herdr call names its session explicitly, and Herdr 0.9.5 is the version floor](decisions/explicit-herdr-session-and-version-floor.md) |
| D19 | [An attempt's version 1 result file is published by hard link](decisions/version-1-result-file.md) |
| D20 | [Guildhall types the launch command into a new Herdr tab, quoted for the pane's shell](decisions/herdr-typed-launch-command.md) |
| D21 | [The fake Artisan is a subcommand of the guildhall binary](decisions/fake-adapter-as-a-subcommand.md) |
| D22 | [Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow](decisions/workflow-approvals.md) |
| D23 | [A typed decision is bound to the artefact its answer applies to, with option-checked answers](decisions/typed-decisions-bound-to-an-artefact.md) |
| D24 | [Inspection reads take their times from store receipts](decisions/inspection-reads-from-store-receipts.md) |
| D25 | [Attempt prompts and skills are rendered once into self-contained files with platform-independent digests](decisions/rendered-prompts-skills-and-digests.md) |
| D26 | [Who fulfils a step is an actor setting, not part of the step kind's name](decisions/actor-as-a-step-setting.md) |
| D27 | [The interpreter owns its launch seam, and one scripted mock agent stands in for workers in tests](decisions/scripted-mock-agent-and-launch-seam.md) |
| D28 | [The launcher owns each worker through one containment interface with a backend per platform](decisions/artisan-containment.md) |
| D29 | [The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes](decisions/grill-live-interview.md) |
| D30 | [Release skills live in the top-level `skills/` directory and are embedded in the binary](decisions/release-skills-root.md) |
| D31 | [Attempt ownership lives in the store](decisions/store-attempt-ownership.md) |
| D32 | [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md) |
| D33 | [Linux containment uses a delegated cgroup v2, else a subreaper and process group](decisions/linux-cgroup-containment.md) |
| D34 | [Each attempt's control endpoint is one AF_UNIX socket, on Windows as on Unix](decisions/attempt-endpoint-af-unix.md) |
| D35 | [Loops and the review gate are derived from recorded attempt bindings](decisions/loops-and-review-gate.md) |
| D36 | [An attempt's prepared files are published once and never replaced](decisions/immutable-attempt-preparation.md) |
| D37 | [Containment persists as a recovery description, not a name](decisions/containment-persistence.md) |
| D38 | [The attempt control protocol is versioned and decoded strictly](decisions/endpoint-protocol-v1.md) |
| D39 | [One strict JSON decoder serves Guildhall's JSON formats](decisions/shared-strict-json.md) |
| D40 | [Director rulings are store records, and a contested blocker opens the loop exit early](decisions/director-rulings.md) |
| D41 | [The launcher decides each attempt once, from its files before its processes](decisions/stop-ladder-precedence.md) |
| D42 | [Rebase classification runs the git program through an internal/git boundary](decisions/rebase-classification.md) |
| D43 | [Full-review requests are store records consumed by a round](decisions/full-review-request.md) |
| D44 | [Build and fix stages use the guildhall-contract-tdd skill, adapted from upstream tdd](decisions/contract-tdd-skill.md) |
| D45 | [`internal/launch` is the only place the owned attempt commands are composed](decisions/launch-composition.md) |
| D46 | [A CI runner's required tools turn their skips into failures](decisions/ci-required-skips.md) |
| D47 | [The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs](decisions/review-skill.md) |
| D48 | [Artisan resume sweeps a recorded unit only after the owner-gone rule](decisions/artisan-resume.md) |
| D49 | [The Guildmaster and other Directors send a Director typed messages over SSH, which inform but never start work](decisions/director-messaging.md) |
| D50 | [A Director runs standalone or managed, and no Director feature requires a Steward](decisions/optional-steward.md) |
| D51 | [Releases install side by side, a per-copy pin selects one, and tasks keep the release they started on](decisions/release-upgrades.md) |
| D52 | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| D53 | [One command grammar for the Director, Artisan and Steward roles](decisions/cli-role-grammar.md) |
| D54 | [The store owns "the first finish stands"](decisions/finish-first-stands.md) |
| D55 | [The Claude adapter runs print mode under a wrapper that owns the result](decisions/claude-adapter-wrapper.md) |
| D56 | [Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow](decisions/workflow-approvals.md) |
| D57 | [Plain English to the Director is the Guildmaster's primary interface, and the commands stay complete and public](decisions/director-conversation-first.md) |
| D58 | [One package owns the runtime names](decisions/runtime-adapter-owner.md) |
| D59 | [Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat](decisions/stall-observation.md) |
| D60 | [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md) |
| D61 | [The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes](decisions/grill-live-interview.md) |
| D62 | [A failed or stopped attempt has a next attempt, started by step retry](decisions/failed-attempt-retry.md) |
| D63 | [Role prompts and stage guides ship with the binary](decisions/release-baseline.md) |
| D64 | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| D65 | [The Director's status feed is one store read plus files, with `needs_me` in a fixed order](decisions/director-status-feed.md) |
| D66 | [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md) |
| D67 | [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md) |
| D68 | [A task's working directory is a git worktree of its repository](decisions/worktree-custody.md) |
| D69 | [One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input](decisions/director-wake-hook.md) |
| D70 | [Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat](decisions/stall-observation.md) |
| D71 | [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md) |
| D72 | [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md) |
| D73 | [Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped](decisions/director-pause-stop.md) |
| D74 | [Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off](decisions/guard-rails.md) |
| D75 | [The release ships five Director skills split by moment, and tests hold them to the README's commands](decisions/director-skills.md) |
| D76 | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| D77 | [One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input](decisions/director-wake-hook.md) |
| D78 | [`director start --tab` types the supervisor into one Director tab it owns, and replaces only shells it can prove it created](decisions/director-tab.md) |
| D79 | [Each attempt records the tokens and cost its runtime reports, marked measured, estimated or unknown](decisions/attempt-spend.md) |
| D80 | [The Director safety checks drive the built binary with stand-in runtimes](decisions/director-safety-checks.md) |
| D81 | [Tests skip the store's fsync, scale wait ceilings on CI and share fixtures, to keep Windows CI fast and steady](decisions/windows-ci-speed.md) |
| D82 | [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md) |
| D83 | [The quality rubric ships as a document and a Director skill, each practice behind a setting, with model scores from a separate judge attempt](decisions/quality-rubric.md) |
| D84 | [Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped](decisions/director-pause-stop.md) |
| D85 | [Three triggers tell the Director a handoff is due, and Claude's context is read from the transcript by the core turn-end hook](decisions/director-handoff-triggers.md) |
| D86 | [The session supervisor watches pending checks, and resume wakes the Director once](decisions/director-checks-watch.md) |
| D87 | [No close opens the Guildmaster's audit of a delivered task, and recorded audits stay answerable](decisions/retire-quality-audit.md) |
| D88 | [One registry in the store lists the message types Guildhall sends, and the engine refuses any other](decisions/message-type-registry.md) |
| D89 | [Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant](decisions/task-grants.md) |
| D90 | [The Director's runtime runs in a containment unit recorded on its session before its program can run](decisions/director-runtime-record.md) |
| D91 | [A profile's model and effort reach its runtime by value, effort falls back to a Guildhall setting, and a runtime refuses what it cannot honour](decisions/profile-model-effort.md) |
| D92 | [The Director parks a failed or stopped attempt with a reason, so it leaves needs_me](decisions/park-attempt.md) |
| D93 | [Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant](decisions/task-grants.md) |
| D94 | [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md) |
| D95 | [A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance](decisions/task-kinds.md) |
| D96 | [A parked attempt, a deferred decision or a follow-up can be held until named tasks merge](decisions/hold-until-merged.md) |
| 0001-one-go-module-and-one-binary.md | [The repository is one Go module that builds one binary](decisions/one-go-module-and-one-binary.md) |
| 0002-go-1-27.md | [The module targets Go 1.27](decisions/go-1-27.md) |
| 0003-standard-library-only-for-the-foundation.md | [The foundation uses only the standard library, and dependencies arrive with the feature that needs them](decisions/standard-library-only-for-the-foundation.md) |
| 0004-ci-matrix-and-lint.md | [CI builds, vets, tests and lints on Linux, Windows and macOS with a pinned golangci-lint](decisions/ci-matrix-and-lint.md) |
| 0005-release-builds.md | [Releases are five static cross-compiled binaries with checksums, published only from a version tag](decisions/release-builds.md) |
| 0006-doctor-reports-only-what-it-checked.md | [`doctor` reports only what it checked, and a missing tool is not a failure](decisions/doctor-reports-only-what-it-checked.md) |
| 0007-release-policy-regression-test.md | [The release policy test parses the workflow with Python and PyYAML, as development-only dependencies](decisions/release-policy-regression-test.md) |
| 0008-toml-parsing-for-copy-configuration.md | [Copy configuration is parsed with BurntSushi/toml](decisions/toml-parsing-for-copy-configuration.md) |
| 0009-bbolt-for-the-store.md | [The store uses bbolt, with JSON records and no generic storage interface](decisions/bbolt-for-the-store.md) |
| 0010-store-single-writer-lock.md | [The store's single-writer rule uses bbolt's own file lock](decisions/store-single-writer-lock.md) |
| 0011-repository-paths-are-absolute-in-the.md | [Repository paths are absolute in the owner machine's form](decisions/repository-paths-are-absolute.md) |
| 0012-steward-rollouts-copy-files-with-scp.md | [Steward rollouts copy files with scp and run no remote command](decisions/steward-rollouts-copy-with-scp.md) |
| 0013-rollout-plans-previews-and-receipts-live.md | [Rollout plans, previews and receipts live in one plan directory](decisions/rollout-plans-previews-and-receipts.md) |
| 0014-separate-managed-publication-from-director-owned.md | [Rollouts write only `managed.toml`, separate from the Director-owned `config.toml`](decisions/managed-publication-separate-from-director-config.md) |
| 0015-local-messaging-in-the-store.md | [Director and Artisan messages are records in the Director's store](decisions/local-messaging-in-the-store.md) |
| 0016-contract-first-development-and-package-layout.md | [Guildhall is developed contract first, with packages organised by domain](decisions/contract-first-development-and-package-layout.md) |
| 0017-herdr-through-its-command-line-api.md | [Guildhall drives Herdr through its command-line API](decisions/herdr-through-its-command-line-api.md) |
| 0018-explicit-herdr-session-and-version-floor.md | [Every Herdr call names its session explicitly, and Herdr 0.9.5 is the version floor](decisions/explicit-herdr-session-and-version-floor.md) |
| 0019-version-1-result-file.md | [An attempt's version 1 result file is published by hard link](decisions/version-1-result-file.md) |
| 0020-typing-the-launch-command-into-a.md | [Guildhall types the launch command into a new Herdr tab, quoted for the pane's shell](decisions/herdr-typed-launch-command.md) |
| 0021-fake-adapter-as-a-subcommand.md | [The fake Artisan is a subcommand of the guildhall binary](decisions/fake-adapter-as-a-subcommand.md) |
| 0022-workflow-approvals-per-director-keyed-by.md | [Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow](decisions/workflow-approvals.md) |
| 0023-typed-decisions-bound-to-an-artefact.md | [A typed decision is bound to the artefact its answer applies to, with option-checked answers](decisions/typed-decisions-bound-to-an-artefact.md) |
| 0024-inspection-reads-from-store-receipts.md | [Inspection reads take their times from store receipts](decisions/inspection-reads-from-store-receipts.md) |
| 0025-rendered-prompts-skills-and-digests.md | [Attempt prompts and skills are rendered once into self-contained files with platform-independent digests](decisions/rendered-prompts-skills-and-digests.md) |
| 0026-actor-as-a-step-setting.md | [Who fulfils a step is an actor setting, not part of the step kind's name](decisions/actor-as-a-step-setting.md) |
| 0027-scripted-mock-agent-and-launch-seam.md | [The interpreter owns its launch seam, and one scripted mock agent stands in for workers in tests](decisions/scripted-mock-agent-and-launch-seam.md) |
| 0028-artisan-containment.md | [The launcher owns each worker through one containment interface with a backend per platform](decisions/artisan-containment.md) |
| 0029-grill-step-skills.md | [The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes](decisions/grill-live-interview.md) |
| 0030-release-skills-root.md | [Release skills live in the top-level `skills/` directory and are embedded in the binary](decisions/release-skills-root.md) |
| 0031-store-attempt-ownership.md | [Attempt ownership lives in the store](decisions/store-attempt-ownership.md) |
| 0032-deterministic-advance-straight-path.md | [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md) |
| 0033-linux-cgroup-containment.md | [Linux containment uses a delegated cgroup v2, else a subreaper and process group](decisions/linux-cgroup-containment.md) |
| 0034-attempt-endpoint-af-unix.md | [Each attempt's control endpoint is one AF_UNIX socket, on Windows as on Unix](decisions/attempt-endpoint-af-unix.md) |
| 0035-loops-and-review-gate.md | [Loops and the review gate are derived from recorded attempt bindings](decisions/loops-and-review-gate.md) |
| 0036-immutable-attempt-preparation.md | [An attempt's prepared files are published once and never replaced](decisions/immutable-attempt-preparation.md) |
| 0037-containment-persistence.md | [Containment persists as a recovery description, not a name](decisions/containment-persistence.md) |
| 0038-endpoint-protocol-v1.md | [The attempt control protocol is versioned and decoded strictly](decisions/endpoint-protocol-v1.md) |
| 0039-shared-strict-json.md | [One strict JSON decoder serves Guildhall's JSON formats](decisions/shared-strict-json.md) |
| 0040-director-rulings.md | [Director rulings are store records, and a contested blocker opens the loop exit early](decisions/director-rulings.md) |
| 0041-stop-ladder-precedence.md | [The launcher decides each attempt once, from its files before its processes](decisions/stop-ladder-precedence.md) |
| 0042-rebase-classification.md | [Rebase classification runs the git program through an internal/git boundary](decisions/rebase-classification.md) |
| 0043-full-review-request.md | [Full-review requests are store records consumed by a round](decisions/full-review-request.md) |
| 0044-contract-tdd-skill.md | [Build and fix stages use the guildhall-contract-tdd skill, adapted from upstream tdd](decisions/contract-tdd-skill.md) |
| 0045-launch-composition.md | [`internal/launch` is the only place the owned attempt commands are composed](decisions/launch-composition.md) |
| 0046-ci-required-skips.md | [A CI runner's required tools turn their skips into failures](decisions/ci-required-skips.md) |
| 0047-review-skill.md | [The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs](decisions/review-skill.md) |
| 0048-artisan-resume.md | [Artisan resume sweeps a recorded unit only after the owner-gone rule](decisions/artisan-resume.md) |
| 0049-director-messaging.md | [The Guildmaster and other Directors send a Director typed messages over SSH, which inform but never start work](decisions/director-messaging.md) |
| 0050-optional-steward.md | [A Director runs standalone or managed, and no Director feature requires a Steward](decisions/optional-steward.md) |
| 0051-release-upgrades.md | [Releases install side by side, a per-copy pin selects one, and tasks keep the release they started on](decisions/release-upgrades.md) |
| 0052-task-start-and-step-done.md | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| 0053-cli-role-grammar.md | [One command grammar for the Director, Artisan and Steward roles](decisions/cli-role-grammar.md) |
| 0054-finish-first-stands.md | [The store owns "the first finish stands"](decisions/finish-first-stands.md) |
| 0055-claude-adapter-wrapper.md | [The Claude adapter runs print mode under a wrapper that owns the result](decisions/claude-adapter-wrapper.md) |
| 0056-approval-enforcement-at-task-creation.md | [Workflow approvals are per-Director store records keyed by digest, and task creation admits only an authorized workflow](decisions/workflow-approvals.md) |
| 0057-director-conversation-first.md | [Plain English to the Director is the Guildmaster's primary interface, and the commands stay complete and public](decisions/director-conversation-first.md) |
| 0058-runtime-adapter-owner.md | [One package owns the runtime names](decisions/runtime-adapter-owner.md) |
| 0059-stall-interval-setting.md | [Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat](decisions/stall-observation.md) |
| 0060-delivery-stage.md | [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md) |
| 0061-grill-live-interview.md | [The grill is the Director's live interview of the Guildmaster, recorded as the stage's notes](decisions/grill-live-interview.md) |
| 0062-failed-attempt-retry.md | [A failed or stopped attempt has a next attempt, started by step retry](decisions/failed-attempt-retry.md) |
| 0063-release-baseline.md | [Role prompts and stage guides ship with the binary](decisions/release-baseline.md) |
| 0064-launcher-advances-its-task.md | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| 0065-director-status-feed.md | [The Director's status feed is one store read plus files, with `needs_me` in a fixed order](decisions/director-status-feed.md) |
| 0066-validate-stage.md | [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md) |
| 0067-director-session-record.md | [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md) |
| 0068-worktree-custody.md | [A task's working directory is a git worktree of its repository](decisions/worktree-custody.md) |
| 0069-director-wake.md | [One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input](decisions/director-wake-hook.md) |
| 0070-launcher-stall.md | [Worker stall observation is on by default and the attempt's launcher makes it at each heartbeat](decisions/stall-observation.md) |
| 0071-director-start.md | [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md) |
| 0072-director-stages.md | [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md) |
| 0073-director-pause-stop.md | [Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped](decisions/director-pause-stop.md) |
| 0074-guard-rails.md | [Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off](decisions/guard-rails.md) |
| 0075-director-skills.md | [The release ships five Director skills split by moment, and tests hold them to the README's commands](decisions/director-skills.md) |
| 0076-watcher-advance-failed.md | [Commands and each attempt's launcher advance a task, one advance at a time under the task's lock](decisions/task-advance-and-watcher.md) |
| 0077-wake-outside-the-task-lock.md | [One store-first wake tells the Director of unread messages, delivered to Claude through a rewake hook and never typed into its input](decisions/director-wake-hook.md) |
| 0078-director-tab.md | [`director start --tab` types the supervisor into one Director tab it owns, and replaces only shells it can prove it created](decisions/director-tab.md) |
| 0079-attempt-spend.md | [Each attempt records the tokens and cost its runtime reports, marked measured, estimated or unknown](decisions/attempt-spend.md) |
| 0080-director-safety-checks.md | [The Director safety checks drive the built binary with stand-in runtimes](decisions/director-safety-checks.md) |
| 0081-windows-ci-speed.md | [Tests skip the store's fsync, scale wait ceilings on CI and share fixtures, to keep Windows CI fast and steady](decisions/windows-ci-speed.md) |
| 0082-director-handoff-reload.md | [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md) |
| 0083-quality-rubric.md | [The quality rubric ships as a document and a Director skill, each practice behind a setting, with model scores from a separate judge attempt](decisions/quality-rubric.md) |
| 0084-director-stop-admission.md | [Pause holds new attempts where they are recorded, and stop holds them until every attempt is proven stopped](decisions/director-pause-stop.md) |
| 0085-director-handoff-triggers.md | [Three triggers tell the Director a handoff is due, and Claude's context is read from the transcript by the core turn-end hook](decisions/director-handoff-triggers.md) |
| 0086-director-checks-watch.md | [The session supervisor watches pending checks, and resume wakes the Director once](decisions/director-checks-watch.md) |
| 0087-quality-audit.md | [No close opens the Guildmaster's audit of a delivered task, and recorded audits stay answerable](decisions/retire-quality-audit.md) |
| 0088-message-type-registry.md | [One registry in the store lists the message types Guildhall sends, and the engine refuses any other](decisions/message-type-registry.md) |
| 0089-artisan-allow-rules.md | [Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant](decisions/task-grants.md) |
| 0090-director-runtime-record.md | [The Director's runtime runs in a containment unit recorded on its session before its program can run](decisions/director-runtime-record.md) |
| 0091-profile-model-effort.md | [A profile's model and effort reach its runtime by value, effort falls back to a Guildhall setting, and a runtime refuses what it cannot honour](decisions/profile-model-effort.md) |
| 0092-park-attempt.md | [The Director parks a failed or stopped attempt with a reason, so it leaves needs_me](decisions/park-attempt.md) |
| 0093-shipped-default-grants.md | [Claude Artisans get extra tools only from the release and their Director: a shipped default, profile and repository grants, and an approved task grant](decisions/task-grants.md) |
| 0094-director-notes.md | [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md) |
| 0095-bug-workflow-without-acceptance.md | [A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance](decisions/task-kinds.md) |
| 0096-hold-until-merged.md | [A parked attempt, a deferred decision or a follow-up can be held until named tasks merge](decisions/hold-until-merged.md) |
| artisan-child-workspaces.md | [Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished](decisions/artisan-herdr-workspaces.md) |
| artisan-workspace-close-sweep.md | [Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished](decisions/artisan-herdr-workspaces.md) |
| artisan-workspaces-readable-stream.md | [Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished](decisions/artisan-herdr-workspaces.md) |
| bound-revision-and-role-profiles.md | [Tasks resolve their bound revision by name, and role profiles are resolved at use, outside the digest, with Guildhall shipping them](decisions/role-profiles-outside-the-digest.md) |
| build-head-output.md | [The engine advances a task deterministically from its store records, and runs Director stages as attempts](decisions/engine-advance.md) |
| ci-timing-flakes.md | [An owned Artisan's timeout starts after setup, and a launcher that never started a worker records `not-started` evidence](decisions/artisan-timeout-and-not-started-evidence.md) |
| director-startup-admission.md | [The Director runs as a supervised session, recorded in the store, that recovers before it starts new work](decisions/director-start.md) |
| director-tab-replacement.md | [`director start --tab` types the supervisor into one Director tab it owns, and replaces only shells it can prove it created](decisions/director-tab.md) |
| effort-default.md | [A profile's model and effort reach its runtime by value, effort falls back to a Guildhall setting, and a runtime refuses what it cannot honour](decisions/profile-model-effort.md) |
| forge-gh-command-syntax.md | [forge contract: command syntax audit](contracts/forge.md#command-syntax-audit) |
| lanterns-and-core-context.md | [Lanterns is a visuals-only plugin in every Claude Director workspace, with the S2 smithy and Calm on by default](decisions/lanterns-visuals.md) |
| lanterns-calm-s2.md | [Lanterns is a visuals-only plugin in every Claude Director workspace, with the S2 smithy and Calm on by default](decisions/lanterns-visuals.md) |
| launcher-stop-reply-drain.md | [endpoint contract: shutdown drain](contracts/endpoint.md#shutdown-drain) |
| merge-requires.md | [Delivery publishes a task branch, validates it on the forge's checks and squash-merges it under the repository's merge requirements](decisions/delivery-stage.md) |
| no-silent-fake.md | [Tasks resolve their bound revision by name, and role profiles are resolved at use, outside the digest, with Guildhall shipping them](decisions/role-profiles-outside-the-digest.md) |
| plain-reload-ignores-director-stages.md | [A Director keeps small notes as it goes and reloads at the end of a turn, never held by a running Director stage](decisions/director-reload-and-notes.md) |
| pr-conflict-director-control.md | [Authorized pull requests wait in a merge queue, and Guildhall rebases the front itself in three tiers](decisions/merge-queue-rebase.md) |
| preflight-lock-wait-test-budget.md | [test timing: preflight lock wait](test-timing.md#preflight-lock-wait) |
| quality-judge.md | [The quality rubric ships as a document and a Director skill, each practice behind a setting, with model scores from a separate judge attempt](decisions/quality-rubric.md) |
| queue-over-cap-starts.md | [Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off](decisions/guard-rails.md) |
| research-build-instructions.md | [A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance](decisions/task-kinds.md) |
| research-profile.md | [A task is created as a feature, bug or hallmark kind, and the shipped bug workflow runs with no grill and no acceptance](decisions/task-kinds.md) |
| review-base-input.md | [The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs](decisions/review-skill.md) |
| review-verdict-routes.md | [A review outcome is guildhall.review-outcome/2, and a changes_requested verdict always loops back](decisions/review-outcome-2.md) |
| unconfigured-review-revisions.md | [The guildhall-review skill adapts upstream code-review to a stage that supplies its inputs](decisions/review-skill.md) |
| unlimited-daily-attempts.md | [Guard rails are enforced by the binary: a start over the running cap is queued, and the daily cap may be turned off](decisions/guard-rails.md) |
| workflow-artisan-tabs.md | [Each workflow Artisan gets its own Herdr workspace below its Director, streams plain output, and closes once finished](decisions/artisan-herdr-workspaces.md) |
| workflow-model-and-encoding-review.md | [workflow model and encoding review (evidence)](evidence/workflow-model-and-encoding-review-2026-10-30.md) |
