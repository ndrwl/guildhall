# Configuration and workflows

Each Guildhall copy has a local TOML config file, and enrolled rollout copies also have a separate Steward-owned `managed.toml`; each Director has JSON workflow definitions.
Pure parsing APIs in `internal/config` and `internal/workflow` run nothing and grant no authority.
`config.Load` reads the config and its sibling managed file when present, while `config.ParseCombined` validates supplied bytes without file access.
Legacy `config.Parse` still accepts inline managed settings, and `config.Load` falls back to it when no sibling `managed.toml` exists.
When that sibling exists, `config.Load` uses `config.ParseCombined`, which refuses inline managed settings with migration guidance.
Only a sibling that does not exist falls back: a sibling that exists but cannot be read, such as a directory, or that `config.ParseCombined` refuses, such as one with a checksum mismatch, is an error rather than a silent return to the inline layout.
A config file that cannot be read is an error.
No credentials belong in either; sign-in stays in each runtime's own store.
Explicit standalone/managed admission, migration, enrolment and revision authorization are designed, not built (see [standalone and managed Directors](director-modes.md)); they do not change the current parser behavior described above.

## Config file

The config is versioned by `schema_version`.
A config with a newer `schema_version` than this Guildhall supports is refused rather than guessed at, and unknown keys are refused.
See [`examples/director.toml`](../examples/director.toml) for a Director's local config and [`examples/managed.toml`](../examples/managed.toml) for an initially empty managed file.
The managed file's first line binds the exact remaining bytes by SHA-256; use `config.EncodeManaged` to generate it, rather than editing its payload without regenerating the checksum.

| Key | Meaning |
|---|---|
| `schema_version` | Required; currently `1`. |
| `mode` | `steward` or `director`. |
| `name` | The Director's name; required in director mode. |
| `[private_config] location` | Steward only: where the private config repository lives, as a path or remote; never a credential. |
| `[managed.<key>]` | In the separate managed file for [Steward rollouts](steward.md): `value`, and `overridable` (default `false`). |
| `[local]` | Owned by the copy: `<key> = "value"`. |
| `[[repository]]` | Director only: one entry per repository the Director owns. |
| `[profile.<name>]` | Director only: a named Artisan [profile](#profiles). |

Keys whose names look like credentials, such as `token` or `password`, are refused in both sections.

### Precedence

A local value replaces a managed value only when that managed key is marked `overridable = true`.
A local value for a managed key that is not overridable is refused, so a managed rule is never silently overridden.
A local key with no managed counterpart is the copy's own setting.

### Settings

These are the settings Guildhall itself reads, by their effective value under the precedence above; `config.Parse` refuses a value it cannot use.
Each has a [scope](#setting-scopes): `task` or `machine`.

| Key | Scope | Meaning |
|---|---|---|
| `default_effort` | task | Fallback effort for the Director session (including a positional runtime with no `[director]` table) and workflow Artisan stages whose profile has unset or empty `effort`: `low`, `medium`, `high`, `xhigh` or `max`, default `medium`; `config.EffectiveEffort` resolves it. Normal managed/local precedence applies, and empty, padded or unknown values in either section are refused even if overridden. An explicit `[director] effort` or profile `effort` replaces this fallback and is checked by its runtime. |
| `stall_interval` | task | How long a launched worker may go without output, while its launcher's heartbeat stays fresh, before its launcher, or an advance, sends the Director a [`worker-stalled`](interpreter.md#worker-stalled-observations) message: a Go duration such as `"15m"` or `"90s"`, default 15 minutes; `"0s"` turns the observation off, and `config.StallInterval` reads it. |
| `max_running_attempts` | machine | The [guard rail](director.md#guard-rails) capping how many Artisan attempts may run at once across the Director's repositories: a positive whole number such as `"4"`, the default 4; `config.GuardRails` reads it. |
| `max_attempts_per_day` | machine | The guard rail capping how many Artisan attempts the Director may start in one UTC day: a whole number, the default 40; `"0"` means no daily cap. |
| `herdr_session` | machine | The named Herdr session for [the Director's tab](director.md#the-directors-tab) and [workflow Artisan workspaces](artisan.md#workflow-artisan-workspaces), letters, digits, `-` or `_`; Herdr's `default` session is refused, and `config.HerdrSession` reads it. Unset, `director start --tab` is refused and `director start` runs the session in the current terminal. |
| `artisan_workspace_close_after` | machine | How long after an attempt finishes the session supervisor's [workspace close sweep](artisan.md#workflow-artisan-workspaces) closes its Herdr workspace, once its launcher is proven gone: a Go duration such as `"5m"` or `"1h"`, default 5 minutes; `"0"` or `"0s"` never closes one automatically, and `config.ArtisanWorkspaceCloseAfter` reads it. The supervisor reads it when it admits the config, at `director start` and each reload. |
| `prune_after` | machine | How long after a task closes [`guildhall prune`](running.md#pruning) may reclaim its worktree, attempt files and CI log cache: a Go duration such as `"336h"`, default 336 hours (14 days); `"0"` or `"0s"` makes a task prunable as soon as it closes, and `config.PruneAfter` reads it. |
| `prune_daily` | machine | `"true"` makes the session supervisor run the same prune as `guildhall prune` at session start, when the copy's last prune was a day ago or more, and then once a day; default `"false"`, and `config.PruneDaily` reads it. Like `artisan_workspace_close_after`, the supervisor reads both prune settings when it admits the config, at `director start` and each reload. |
| `lock_wait` | machine | How long each advance (`task start`, `step done`, `step finish`, `step retry` and a launcher's [watcher advance](running.md#the-watcher)) waits for its task lock, and for the store's lock at each open (including preflight hold/admission reads and launch ownership polling), before it fails: a positive Go duration, default 30 seconds; `config.LockWait` reads it. A launcher's advance reads the same copy, so a failed watcher advance and its one retry each wait this long. |
| `wake_quiet` | machine | How long the Director session's supervisor holds a burst's [message wakes](messaging.md#waking-the-director) after the newest before it wakes the Director once for them: a Go duration, default 3 seconds; `"0s"` turns holding off, so every message wakes the Director at once, and `config.WakeQuiet` reads it. The supervisor reads it at `director start` and each reload. |
| `wake_max_delay` | machine | How long after the first held message wake the supervisor wakes the Director, however long the burst goes on: a positive Go duration no less than `wake_quiet`, default 15 seconds; `config.WakeMaxDelay` reads it. |
| `checks_refresh` | task | How long a [validate](interpreter.md#validate) step waiting on a head's checks waits after reading them before an advance reads them again: a Go duration, default 1 minute; `"0s"` reads them at every advance, and `config.ChecksRefresh` reads it. The Director session's [checks watcher](director.md#checks) also uses this setting, with its own minimum interval. |
| `ci_log_limit` | task | How many bytes of each [failed job log](interpreter.md#validate) Guildhall fetches into the CI log cache, at validate and in [`ci log`](ci-logs.md), keeping the log's tail: whole bytes with an optional `KiB`, `MiB`, `KB` or `MB` suffix, all 1024-based, such as `"5MiB"`, `"512KiB"` or `"1048576"`, from 64 KiB to 100 MiB, default 5 MiB; `"0"` fetches no logs, so each failed check's log is unavailable. Negative, fractional, padded and unknown-suffix values are refused in either section, and `config.CILogLimit` reads it. |
| `standards_revision` | machine | The central standards revision each [review](#revisions) is bound to, recorded as its `revisions.standards_revision`; unset is `""`, no standards revision. Any string is accepted. |

The [Claude skill policies](#claude-skill-policies) are machine-wide, and the [quality settings](#quality-settings) are task-scoped.

### Setting scopes

Every setting Guildhall reads is declared once, in `config`'s setting registry, as task-scoped or machine-wide; a test fails for a setting key with no scope.
A **task-scoped** setting is one Guildhall reads while running a particular task, so a task override may replace it for that task.
A **machine-wide** setting is read for the whole copy, outside any one task, so no task can override it: the Director session's supervisor, guard rails, locks, Herdr, prune, wakes, the Claude skill policies, which the Claude wrapper re-reads at launch, and `standards_revision`, which binds reviews.
`[repository.merge]` and all its keys, including the rebase settings, `[director]` and a repository's identity (`id`, `path`, `remote`, `owner_machine`) are not settings and are never overridable per task either.

`config.WithTaskOverrides` gives a copy of a config whose task-scoped settings take a task's overrides first: task override, then local, then managed, where a managed key that is not `overridable = true` still wins.
It never changes the config it is given.
An override that does not apply when it is read, because its key is unknown, machine-wide, kept out or pinned by managed config, or its value fails the setting's check or leaves the config invalid, is skipped with its reason rather than failing.
`config.CheckTaskOverride` refuses such an override with that reason.

### Claude skill policies

`claude_skills_director`, `claude_skills_artisan` and `claude_skills_profile_<declared-name>` are string-valued managed/local settings containing a typed JSON policy.
Each shipped default is overridable; a Steward can lock a key with `overridable = false` using normal managed/local precedence.
Managed values are supported by the configuration resolver; current standalone Director admission still refuses managed copies as described above.
Local replacement replaces the entire policy value, not individual JSON members.
A profile-specific effective key replaces the generic Artisan key; otherwise the profile inherits that generic policy, which defaults to `artisan-v1`, without guessing roles from names like `reviewer-opus-b`.
The shipped example explicitly binds researcher/planner to `artisan-v1`, builder to `builder-v1` and reviewer to `reviewer-v1`.

| Preset | Bundled skills kept | Account sync | Plugin-authoring builtin |
|---|---|---|---|
| `director-v1` | None; all 13 Guildhall release skills remain independent | Off | Disabled |
| `artisan-v1` | None; rendered workflow skill documents remain available | Off | Disabled |
| `builder-v1` | `claude-api`, `run`, `dataviz` | Off | Disabled |
| `reviewer-v1` | `security-review`, supplementary only, never a substitute for Guildhall review | Off | Disabled |

```toml
[local]
claude_skills_profile_builder = '{"preset":"builder-v1","overrides":{"dataviz":"off","simplify":"on"}}'
# Explicit document opt-in; do not silently inherit the signed-in account everywhere.
claude_skills_profile_researcher = '{"preset":"artisan-v1","account_sync":true,"overrides":{"anthropic-skills:pdf":"on","anthropic-skills:docx":"on"}}'
```

The only policy fields are required `preset`, optional `account_sync` (false by default), `overrides` (canonical skill name to `on`, `off`, `name-only` or `user-invocable-only`) and `plugins` (explicit `plugin@source` IDs to booleans).
Account skills use the `anthropic-skills:` namespace; keeping one without `account_sync = true` is an error.
PDF/Office/browser work is explicit opt-in: select the appropriate account skill and sync, and for browser/computer work enable its backing capability together with its safety instructions.
An opt-in may expose posting, scheduling, connected data and spend; permissions still govern tools.
Unknown custom skill names are accepted but reported as not discovered, not guaranteed available.
Malformed JSON, duplicate keys, unknown fields, invalid states/names, unsupported presets and explicit profile policy on a non-Claude profile are refused even in inactive/overridden values.

Presets use `disableBundledSkills` when no bundled skill is kept.
Keeping any known bundle switches to selective `skillOverrides`, suppressing the other known bundles; unknown future bundles may then load.
Account sync defaults off via session `--settings`, stopping loading of cached account skills too, without changing account/global files.
Account-enabled policies suppress known unwanted account skills but cannot promise to suppress newly synced unknown names.
Plugin skills are excluded from `skillOverrides`: disable a whole plugin through `plugins`, not a skill called `plugin-authoring`.
The presets disable only `cc-plugin-plugin-authoring@builtin`; unrelated plugins, including Guildhall Lanterns, are untouched.
No blanket `Skill` deny is generated: selection is ergonomics, not a security sandbox, and existing tool denies remain authoritative.
An organization-managed force-enabled plugin may override these controls.
Personal/project skills, unknown plugins and feature-gated inventory are not isolated by this policy.

Resolved settings are rendered before launch, not edited in generated workspaces.
Both Director and Artisan pass an owned settings file with `--settings`, even when Artisan grants are empty, and record policy provenance/digest in `claude-skill-policy.json`.
`director start --check` reports configured policy and the tested Claude Code baseline (minimum 2.1.310), not a paid live inventory probe; unsupported versions refuse launch because print mode can silently ignore unsupported settings.
A kept-bundle policy also refuses an inherited `CLAUDE_CODE_DISABLE_BUNDLED_SKILLS` kill switch.
The researcher profile's [shipped default grant](adapters.md#artisan-permissions) includes `WebSearch` and `WebFetch`; the planner, builder and reviewer defaults have no web grants.
Customize those tool grants in the copy's profile, independently from managed/local skill selection.

### Quality settings

These settings switch the optional practices of the [quality rubric](quality.md) and size them ([decision](decisions/0083-quality-rubric.md)); `config.Quality` reads their effective values and `config.Parse` refuses a value it cannot use, naming the key.
A copy that sets none of them gets the defaults below, with self-review and reference runs off.
[`director status`](director.md#status) shows the effective values as its `quality` section.

| Key | Meaning |
|---|---|
| `quality_self_review` | `on` runs the [`guildhall-self-review`](../skills/guildhall-self-review/SKILL.md) skill after each merged task; `off`, the default, never does. |
| `quality_size_budget_lines` | The soft size budget's non-test lines changed: a whole number, default 400; a change over it is flagged in the self-review, never blocked, and `"0"` turns that half of the flag off. |
| `quality_size_budget_files` | The soft size budget's files touched, the same way: default 15. |
| `quality_audit_sample` | Independently of `quality_self_review`, about one in this many delivered tasks, chosen by the task's identity, is sampled for the Guildmaster's three-question [`audit` decision](interpreter.md#publish-and-deliver), as are reference runs once they exist: a whole number, default 5; `"0"` turns the audit off. |
| `quality_reference_runs` | When the reference task set runs on a real model: `off`, the default, or `on-change-and-release`, for each workflow-change proposal and each release tag. |
| `quality_reference_runs_per_task` | How many times each reference task runs per workflow version: a positive whole number, default 3. |
| `quality_reference_cost_cap_usd` | The cap on one reference evaluation's cost in US dollars, such as `"25"` or `"12.50"`; unset by default, and required while `quality_reference_runs` is not `off`. |
| `quality_judge_profiles` | The declared [profiles](#profiles) a [judge session](quality.md#running-it) may run as, in order of preference, separated by commas, such as `"judge-codex, judge-opus"`; a profile whose judge of the same self-review failed is passed over, so a judge out of quota falls back to the next; the builder's profile is excluded, and an unset list or no eligible profile refuses the judge with `ErrNoJudge`; a name the config does not declare, or one named twice, is refused. |
| `quality_judge_separate_family` | `on`, the default, prefers the first eligible listed judge profile of a different model family from the builder's when the builder's historical adapter is known, a family being a profile's adapter; otherwise it takes the first eligible profile. See [running the judge](quality.md#running-it) for the current historical-family limitation. `off` takes the first eligible listed profile, never the builder's session. |
| `quality_skill_edits_need_approval` | `on`, the default for the first cycle, treats an edit to the review, build or spec skill text as a design change that needs the Guildmaster's approval; `off` treats it as any other lesson. |

`quality_self_review` and the size budget are read by the self-review skill, `quality_audit_sample` by deliver as it closes a merged task, and the judge settings by [`quality judge`](quality.md#running-it) ([decision](decisions/quality-judge.md)); the other settings are read by nothing yet.
The reference-run and skill-edit settings are parsed and validated so a copy can set them now; the reference runner and the lesson-proposal shape that will read them are later work.

### Repositories

| Key | Meaning |
|---|---|
| `id` | Unique lowercase identifier. |
| `path` | Absolute path of the repository on the owner machine. |
| `remote` | Git remote; web URLs must not embed user info and no URL may embed a password. `publish` pushes task branches there, and the [GitHub forge](contracts/forge.md) supports only `github.com` remotes. |
| `owner_machine` | The Director's one owner machine; every repository of a Director names the same one. |
| `workflow` | Name of the Director's workflow definition a [feature task](tasks.md#task-kinds) runs; defaults to `default`. |
| `[repository.kinds]` | The workflow each other [task kind](tasks.md#task-kinds) runs in this repository, such as `bug = "my-bug"`, resolved like `workflow`; a kind it does not name runs the shipped workflow of that name. `bug` is the only key: `feature` is refused, because `workflow` names it, and so is any other kind. Omitted, a config's digest is unchanged. |
| `no_ci` | `true` declares that the repository runs no CI, so [validate](interpreter.md#validate) passes a published head its forge reports no checks for, recording that it had no CI; default `false`, where no checks reported waits as pending. |
| `[repository.workflow_overrides.<step-id>]` | Chooses that step's `actor` for this repository only. |
| `[repository.merge.requires]` | What a task's pull request needs before Guildhall merges it; see [merge requirements](#merge-requirements). |
| `[repository.merge]` | How Guildhall classifies a rebase and when a rebased head needs a new merge decision; see [rebase settings](#rebase-settings). |
| `[repository.profiles.<name>]` | A repository grant: its `allowed_tools` are added to that profile's effective grant for this repository's tasks only, such as `["Bash(go test:*)", "Bash(go vet:*)"]` for a Go repository's builder ([Artisan permissions](adapters.md#artisan-permissions)). The profile must be one the config declares or one of the [shipped profiles](#profiles), running the `claude` adapter; each rule is checked as a profile's are, and listed once. |

Repository paths accept a POSIX path starting with `/` or a rooted Windows drive-letter path on any parsing host; backslash-prefixed UNC paths are refused.
Two repositories may not use the same path after dot segments are cleaned; drive paths also normalize separators and drive-letter case.
Other letter case is compared exactly.

#### Merge requirements

Each repository chooses what a task's pull request needs before Guildhall merges it ([decision](decisions/merge-requires.md)).
The section sits under the repository's `[[repository]]` block, like `workflow_overrides`:

```toml
[repository.merge.requires]
guildmaster_approval = true   # the Guildmaster said "merge"
artisan_approval     = true   # the review Artisan approved this exact head, accepted spec and standards
ci_green             = true   # required CI passed on this head
nothing_open         = true   # no open findings on the approving reviews and no unanswered decision on the task
```

| Requirement | Holds when |
|---|---|
| `guildmaster_approval` | The Guildmaster answered `merge` on the deliver decision `deliver-<k>` for this head. |
| `artisan_approval` | Every review and document-review step before deliver has a latest outcome that approves and is bound to the delivered head, the accepted spec digest and the current standards revision. "Artisan" means the independent review Artisan: the review step runs as its own attempt, never the author's build attempt. |
| `ci_green` | Deliver's `validation` input is a `passed` result for the delivered head. |
| `nothing_open` | Those review outcomes have no open finding, and the task has no unanswered decision but the deliver step's own. Every finding is open, unresolved or newly raised, except a `disputed` one the Director has ruled on with a ruling that stands. Suggestions never count; the condition's reason still says "N suggestion(s), not counted". Without `guildmaster_approval`, a merge decision opened because this failed is superseded when it comes to hold before the decision is answered, and Guildhall merges by itself. |

Each key is a TOML boolean; a missing key is `true`, and a missing section requires everything, so by default the Guildmaster approves every merge.
A string such as `"true"`, an unknown key and the removed merge policy key are refused when the config is parsed.

- A required `artisan_approval` or `ci_green` that does not hold stops deliver with a review or validation gate error, whatever `guildmaster_approval` says; no answer merges over it.
  Setting it to `false` skips that gate, and the merge question still shows its state.
- A behind or conflicting pull request no longer waits before anything is asked: the decision opens, or the authorization is recorded, and the authorized merge joins the repository's [merge queue](interpreter.md#the-merge-queue), whose front Guildhall [rebases](#rebase-settings).
- With `guildmaster_approval = true`, deliver opens the merge decision, whose question lists each requirement as `holds`, `fails: <reason>` or `not required`; a `merge` answer overrides a failing `nothing_open`, and nothing else.
- With `guildmaster_approval = false` and every other required requirement holding, Guildhall records `deliver-<k>-authorized`, with the requirements in effect and the evidence it read, and merges by itself; otherwise it asks the Guildmaster, beginning the question "Guildhall did not merge by itself".
  A recorded `deliver-<k>-authorized` is reused for its head on every later advance, before `guildmaster_approval` is read: a merge the forge refused after it was authorized is retried without a decision even if `guildmaster_approval` is turned back on. To have the Guildmaster decide instead, publish a new head.
- A head that changes the repository's [policy file](#repository-policies), `.guildhall/policies.md`, against the task's base needs `guildmaster_approval` whatever `merge_requires` says, as a change to review requirements needs it under the [workflow-edit authority](#workflow-edit-authority): an Artisan cannot change its own rules.
  The question lists `guildmaster_approval: required (the head changes .guildhall/policies.md)` and says why; Guildhall never records `deliver-<k>-authorized` by itself for such a head, and a rebase of it follows `reapprove_after_rebase` as if `guildmaster_approval` were set.
  Only the Guildmaster's answer carries over to such a head: an authorization Guildhall recorded by itself for an earlier head, one that did not change the file, never does, even under `reapprove_after_rebase = never`, so the head gets a merge decision instead.
  `artisan_approval`, `ci_green` and `nothing_open` keep their own settings.

Turning a requirement off changes only what deliver demands, not which steps a workflow must have.
No workflow can change a repository's merge requirements.

#### Rebase settings

Guildhall rebases the pull request at the front of a repository's merge queue, and a conflicting one at validate, itself ([tiered rebase](interpreter.md#tiered-rebase), [decision](decisions/merge-queue-rebase.md)).
Three keys under `[repository.merge]`, beside `requires`, say how:

```toml
[repository.merge]
reapprove_after_rebase = "major_conflicts"  # no_conflicts | minor_conflicts | major_conflicts | never
minor_conflict_files   = 3
minor_conflict_lines   = 50
```

| Key | Meaning |
|---|---|
| `reapprove_after_rebase` | The rebase kind from which a rebased head needs a new merge decision: a rebase at or above it asks the Guildmaster again, and one below it carries the earlier approval over, while every gate holds, nothing new is open and the accepted spec is unchanged. The order is `no_conflicts` < `minor_conflicts` < `major_conflicts` < `never`, so `no_conflicts` always asks again and `never` never does. Default `major_conflicts`. |
| `minor_conflict_files` | The most conflicting files a `minor_conflicts` rebase may have; one more is `major_conflicts`. Default 3, at least 1. |
| `minor_conflict_lines` | The most conflicting lines, counted between the conflict markers on both sides without the marker lines, that a `minor_conflicts` rebase may have. Default 50, at least 1. |

- A missing key takes its default, and a missing section is all three defaults.
- An unknown value, a count below 1, an unknown key and `reapprove_after_rebase = "never"` without `artisan_approval` required are refused when the config is parsed.
- `reapprove_after_rebase` matters only where `guildmaster_approval` is required; without it, Guildhall merges a rebased head by itself whenever the requirements hold.
- Red checks and review findings on a rebased head always reach the Guildmaster, whatever the threshold.

A `minor_conflicts` rebase is resolved by an Artisan run under the profile named [`rebaser`](#profiles), the shipped one unless the copy declares its own, so every Director copy resolves minor conflicts.
A config without a `rebaser` profile, which only a config built in memory can be, escalates every minor rebase to `major_conflicts`, a build send-back, and tells the Director why.

#### Repository policies

A repository's policies are the Guildmaster's standing rules for it, such as "Keep the up-to-date branch protection on main until automatic rebases are proven".
They live in the repository, not in `config.toml` or a Director note, so they are versioned, reviewed through pull requests and shared by every Director copy and contributor.

- **File.** `.guildhall/policies.md` at the repository root; the path is fixed. No file means no policies, and nothing about them is shown.
- **Format.** UTF-8 Markdown. Each top-level list item, a line starting with `- `, is one policy, written on one line of at most 1024 bytes, as a note is. Headings, blank lines and paragraphs are commentary and are not shown. An indented line under an item, which would cut a policy short, a blank or duplicate item, an item holding a carriage return, a file over 16 KiB and a file that is not valid UTF-8 are refused.
- **Revision read.** The repository's default branch as last fetched into `refs/guildhall/policies`, refreshed with a bounded fetch at `director start`, at each reload and on each advance pass; an unreachable remote leaves the ref where it was. The file is never read from a task branch or a work tree, so an Artisan's edit in its own branch changes neither its prompts nor the Director's view until it merges. Before the first fetch the policies are "not read yet", which is not an error.
- **Who sees them.** [`director status`](director.md#status) lists them as `repository_policies`, `startup.md` lists them under "Repository policies", and every Artisan and Director-stage prompt for the repository has them in a fenced "Repository policies" section ([rendering](rendering.md#artisan-prompts)). A refused or unreadable file is shown as such and never stops a prompt from rendering, so the pull request that fixes it still gets its prompts.
- **Not enforced.** Policies are guidance: nothing reads their text to decide anything. Enforced behaviour stays in real settings, such as [`merge_requires`](#merge-requirements). This is unlike [Claude skill policies](#claude-skill-policies), which are config settings Guildhall enforces when it launches a runtime.
- **Merge guard.** A head that changes the file needs the Guildmaster's merge answer whatever `merge_requires` says ([merge requirements](#merge-requirements)). A pull request merged outside Guildhall, by a person on the forge, is not guarded; a forge-side rule such as CODEOWNERS is the Guildmaster's choice.

### Profiles

A profile chooses who runs an Artisan stage whose `profile` setting names it.
For the settings a judge profile currently applies at runtime, see [running the judge](quality.md#running-it).
Names are lowercase identifiers, so three review stages can name `reviewer-opus`, `reviewer-opus-b` and `reviewer-astra` and no model name appears in a repository's workflow.

| Key | Meaning |
|---|---|
| `adapter` | Required: the runtime adapter, such as `claude`. |
| `model`, `effort` | The attempt's [runtime settings](adapters.md#runtime-settings), which defines defaults, supported values and refusals. |
| `role` | A role prompt replacing the release's Artisan role: a relative slash-separated path inside the config file's directory. |
| `allowed_tools` | The tool permission rules this Director grants the profile's Artisan beyond its runtime's baseline, such as `["Bash(go test:*)", "Bash(git commit:*)"]`. Unset, a `claude` profile named `builder`, `designer`, `planner`, `rebaser`, `reviewer` or `researcher`, shipped or declared, gets its shipped default grant and one of another name gets nothing; a set list, even `[]`, replaces that default. A repository grant adds to either. Only the `claude` adapter grants them ([Artisan permissions](adapters.md#artisan-permissions), with the default table and the suggested Go repository grant). |

A profile names no credential.
For how the copy/profile reference reaches the runtime, see [Artisan permissions](adapters.md#artisan-permissions).

Each `allowed_tools` entry is a tool name, optionally with a specifier in parentheses, with no control characters and listed once; a rule allowing every shell command, `Bash`, `Bash(*)` or `Bash(:*)`, is refused, so a grant names its commands.
A tool one task needs for a while, such as a probe build's `Bash(go:*)`, is a [task grant](tasks.md#task-grants) the Guildmaster approves, not an edit to a profile here: it reaches only that task's attempts and expires by itself ([decision](decisions/task-grants.md)).
The rules belong to the Director whose copy's `config.toml` sets them, the copy `--copy` or [copy discovery](running.md#copies) selects: two Directors on one machine grant their Artisans different sets, and no machine-wide file, shared file or Steward-managed value grants them.
`config.CheckProfiles` refuses a workflow whose steps name a profile the config does not declare and Guildhall does not ship, listing every such step.
`config.CheckArtisanAdapters` additionally refuses an Artisan stage that resolves no profile, before task creation or advance mutates task records.
Director, Guildmaster and engine stages do not require an Artisan adapter.

Profiles keep role names, and each step type with a role has a **role profile**:

| Step type | Role profile |
|---|---|
| `research` | `researcher` |
| `plan` | `planner` |
| `build` | `builder` |
| `review` | `reviewer` |
| `rebase` (the merge queue's synthetic rebaser step) | `rebaser` |
| `design` (a design round's synthetic designer step) | `designer` |

A step that an Artisan runs and that names no `profile` runs under its type's role profile (`workflow.EffectiveProfile`).
An explicit step profile always wins, a review overridden to the Guildmaster gets none, and other step types, such as `refute` and `document-review`, get none, so they must name a profile and are refused without one.
The role profile is resolved when the step is checked, launched and rendered, never written into the workflow definition, so it is outside the workflow's digest: a task on the shipped default or bug workflow binds the shipped revision byte for byte and needs no approval, and declaring, changing or removing a role profile neither changes which revision a new task binds nor strands an open one ([decision](decisions/role-profiles-outside-the-digest.md)).
That includes the reviewer: changing the `reviewer` profile changes who reviews without the Guildmaster's approval, while a profile written into a review step is still a weakening ([workflow-edit authority](#workflow-edit-authority)).

Guildhall ships the six role profiles, `researcher`, `planner`, `builder`, `reviewer`, `rebaser` and `designer`, each `adapter = "claude"` with no `model`, `role` or `allowed_tools`, so each runs the adapter's default model with its shipped default grant; each has no `effort` but `designer`, whose effort is `high`.
When a Director config is parsed, each shipped profile it does not declare is added to its profiles, so every check that a profile is declared, such as a repository grant, `claude_skills_profile_<name>`, `quality_judge_profiles` and the `director start` preflight, accepts the shipped names; a Steward config gets none.
A copy's own `[profile.<name>]` with a shipped name replaces the shipped profile as a whole, not key by key; one that sets no `allowed_tools` still gets the shipped grant, so choosing only a `model` or `effort` keeps the permissions.
So a copy's `[profile.designer]` without `effort` runs at `default_effort`, not at the shipped `high`.
So a copy with no `[profile.*]` section runs the shipped default and bug workflows, every role step on claude.
To deliberately run a no-model demo, name an explicit profile whose `adapter = "fake"`, or declare the role profiles with it.

One profile is chosen by name rather than by a workflow step: `rebaser` runs the rebaser Artisan of a `minor_conflicts` [rebase](#rebase-settings), the synthetic step `<step>-rebase`, which resolves a few conflicts and reports whether resolving them changed behaviour.
The shipped one runs with the rebaser's shipped grant; the example config shows a copy section giving it a small model and low effort, and repository grants adding a repository's test command.
The narrowed review that follows, `<step>-rebase-review`, runs under the effective profile of the workflow's review step, the `reviewer` role profile when that step names none.
Another is `designer`, which runs the designer Artisan of a [design round](tasks.md#design-rounds), the synthetic step `designer`, which writes the design the Guildmaster decides on before build.
For when profile settings are checked, see [runtime settings](adapters.md#runtime-settings).

### The director table

The optional `[director]` table chooses who runs the Director's own session, which [`director start`](director.md#start) runs.
It belongs to a Director config; a Steward config with one is refused.

| Key | Meaning |
|---|---|
| `adapter` | The runtime adapter, such as `claude`; required to start a session, and a positional runtime overrides it for one command. |
| `model`, `effort` | Session [runtime settings](adapters.md#runtime-settings), with effort fallback defined under [settings](#settings). |
| `role` | A role prompt replacing the copy's `baseline/roles/director.md` and the release's Director role: a relative slash-separated path inside the config file's directory. |
| `idle_after` | How long a session whose runtime has turn hooks may go with no turn signal and no wake before the supervisor counts the Director as [idle](director.md#the-end-of-a-turn), such as after a turn the captain interrupted: a positive Go duration, `5m` when unset. |
| `handoff_after_tasks` | [Automatic handoff](director.md#automatic-handoff) every that many tasks finished in a session: a non-negative integer, `0` (off) when unset. |
| `handoff_context_percent` | Automatic handoff once the runtime reports that much of its context used: an integer from 0 to 100, `0` for off, `70` when unset. |
| `handoff_idle` | Automatic handoff after that long with no turn signal and no wake: a non-negative Go duration, `0s` (off) when unset. |
| `note_audit_every_sessions` | A [note audit](director.md#maintenance) is due that many sessions after the last recorded one: a non-negative integer, `0` for off, `3` when unset. |
| `hallmark_after_merges` | A [hallmark](director.md#maintenance) of a repository is due after that many of its pull requests merged since its last recorded one: a non-negative integer, `0` for off, `4` when unset. |

Like a profile it names no credential.
A config without the table has the same config digest as before the table existed.
The handoff and maintenance settings are recorded on each session when it starts, so a change takes effect at the next session.

## Lanterns presentation preference

The automatically installed [Lanterns plugin](lanterns.md) keeps its per-Director `/calm` preference in `<copy>/director/lanterns-calm`, not in `config.toml`.
In a Director session, whose plugin directory holds the `calm.json` naming that file, an absent preference means Calm is on; `/calm` toggles and remembers both `on` and `off`, so a saved `off` keeps native drawings.
Function hooks still require explicit opt-in with `CLAUDE_CODE_ENABLE_FUNCTION_HOOKS=1`; without it, Calm has no effect.
The preference survives generated workspace refreshes and handoffs.

## Workflow definitions

A workflow defines the steps of one task as an ordered list of built-in step kinds.
It does not replace the task dependency graph: independent tasks still run in parallel, each with its own workflow.
JSON names built-in kinds and gives their settings; it cannot contain commands, code, expressions or new step kinds.
The shipped default, a feature task's workflow, is [`internal/workflow/default.json`](../internal/workflow/default.json); the shipped bug workflow is [`internal/workflow/bug.json`](../internal/workflow/bug.json), the default's steps without grill and the Guildmaster's acceptance, so a bug task has no Guildmaster stage and merges under the repository's [merge requirements](#merge-requirements); build and review take plan's spec, and research, plan, build and review are instructed to reproduce the defect from the task title, proceed on recommended answers or fail back with a question that needs the Guildmaster, test it first and check that test ([task kinds](tasks.md#task-kinds), [decision](decisions/0095-bug-workflow-without-acceptance.md)).
Both name no profiles; Artisan steps run under their [role profiles](#profiles), resolved at use and outside the digest.
[`examples/workflows/human-review.json`](../examples/workflows/human-review.json) illustrates Guildmaster review of the plan and of every change, with reviewer instructions, and omits the default's research step; its Artisan steps run under their role profiles too.

This structural example also leaves profile selection to the copy; see [profiles](#profiles) for the admission requirements.

```json
{
  "schema": "guildhall.workflow/1",
  "name": "small",
  "steps": [
    {"id": "plan", "type": "plan", "version": 1},
    {"id": "accept", "type": "guildmaster-approval", "version": 1,
     "inputs": {"spec": ["plan.spec"]}},
    {"id": "build", "type": "build", "version": 1, "inputs": {"spec": ["accept.spec"]}},
    {"id": "review", "type": "review", "version": 1, "settings": {"actor": "artisan"},
     "inputs": {"spec": ["accept.spec"], "head": ["build.head"]},
     "on": {"changes_requested": {"goto": "build", "max": 3}}},
    {"id": "publish", "type": "publish", "version": 1, "inputs": {"head": ["build.head"]}},
    {"id": "deliver", "type": "deliver", "version": 1,
     "inputs": {"head": ["publish.head"], "review": ["review.outcome"]}}
  ]
}
```

| Field | Meaning |
|---|---|
| `schema` | `guildhall.workflow/<version>`; a newer version is refused. |
| `name`, `steps[].id` | Lowercase identifiers; step ids are unique. |
| `steps[].type`, `steps[].version` | A built-in kind and its version, both required, so a task never silently changes kind version. |
| `steps[].settings` | Only the settings the kind declares, with their declared JSON types, and `actor` where the kind allows more than one actor. |
| `steps[].inputs` | Input port to an array of `"step.output"` references, including for single-input ports. |
| `steps[].on` | Outcome to `{"goto": <step-id>, "max": <rounds>}`. |

Call `workflow.Parse` to decode a definition, then `workflow.Validate` before using it; parsing alone does not validate wiring.
Parsing stops at the first decoding error, while validation reports the semantic problems it finds together.
JSON field names must match their declared spelling exactly, including case.
Together, parsing and validation refuse unknown fields and duplicate keys, unknown kinds or versions, unknown or mistyped settings, missing required settings, a missing or disallowed `actor`, unwired required inputs, inputs that reference unknown steps or outputs, and inputs whose types do not match.
An input can only come from an earlier step, which makes each step's readiness explicit and rules out dependency cycles.
A rule can only send the task back to the same or an earlier step, and `max` must be between 1 and 10, so every loop is bounded.

### Built-in step kinds

The authoritative [built-in registry](../internal/workflow/registry.go), exposed through `workflow.Kinds`, declares each kind's version, typed ports, cardinality, settings and loop outcomes.

`research` accepts `profile`, a [profile](#profiles) name, and `instructions`, a string rendered into the Artisan's prompt; it does not accept `skills`.
`plan`, `refute`, `build`, `document-review` and `review` accept `profile` and optional `skills`, a list of distinct skill names rendered for the Artisan ([rendering](rendering.md#skills)); reviews keep them when swapping actors, and they are unused while the Guildmaster reviews.
`plan`, `build`, `document-review`, `review` and `guildmaster-approval` also accept `instructions` ([decision](decisions/research-build-instructions.md)).
The `spec` input of `build` and `review` takes an accepted spec or, in a workflow with no acceptance step such as the shipped `bug`, plan's spec; the engine checks a spec against its approval only when it comes from an acceptance ([decision](decisions/0095-bug-workflow-without-acceptance.md)).
The workflow schema permits an absent `profile`; task admission enforces the actor-dependent requirement under [profiles](#profiles).
For the grill step's skills support and constraints, see the [workflow contract](contracts/workflow.md).

Each kind declares who may fulfil it, and `Step.Actor` reports who fulfils a step:

| Actor | Meaning |
|---|---|
| `artisan` | A fresh Artisan session is launched for the step. |
| `guildmaster` | A durable pause that waits for the Guildmaster without model calls. |
| `director` | The step runs inside the Director's own session and ends with a recorded document. |
| `engine` | Guildhall runs the step itself, with no model. |

| Kind | Actors | Produces |
|---|---|---|
| `grill` | `director` or `artisan` | notes: the Director interviews the Guildmaster live with `guildhall-grill` and `guildhall-domain-model`; an Artisan grills unattended with `guildhall-grill-unattended` |
| `research`, `plan`, `build` | `artisan` | notes, a spec document, a code head |
| `refute` | `artisan` | a findings document attacking the proposed spec, not a review verdict |
| `document-review` | `artisan` or `guildmaster` | a review outcome for a document, such as the plan's spec |
| `guildmaster-approval` | `guildmaster` | the accepted spec; may take document-review outcomes as `reviews` and refutation documents as `findings` |
| `review` | `artisan` or `guildmaster` | a review outcome for a code head against the spec the build was given: the accepted spec, or plan's spec in a workflow with no acceptance step |
| `rebase` | `artisan` | a code head resolving a rebase's minor conflicts, and `rebase.json`; run by the engine as `<step>-rebase` under deliver or validate ([tiered rebase](interpreter.md#tiered-rebase)), never as a workflow step |
| `publish` | `engine` | the same head, pushed to the repository's remote branch with its pull request opened or updated |
| `validate`, `deliver` | `engine` | a validation result, the landed change |
| `handoff-wait` | `guildmaster` | a handoff document |

The director actor on `grill` is part of the accepted workflow design's stage table (design report section 4.1: grill produces notes, with actor director, a conversation with the Guildmaster, or artisan) and does not conflict with R1.5, because a grill is the Director clarifying requirements with the Guildmaster, not doing the work.
The director grill is a live interview in rounds of numbered questions until the Director and the Guildmaster share an understanding, recorded as the stage's notes (R1.13); the `artisan` actor is the opt-in for a repository meant to run unattended; with `guildhall-grill-unattended` listed in the grill step's `skills`, its Artisan answers its own questions with recommendations the Guildmaster reviews at acceptance ([decision](decisions/0061-grill-live-interview.md)).
A kind with more than one actor requires the `actor` setting; a kind with one actor refuses it.
`publish` comes before `validate` so forge checks have a pushed head to read; it never merges.
`publish` and `deliver` have no settings: the repository's configuration and merge requirements decide where work goes and how it lands.

### Opt-in plan refutation

[`examples/workflows/refute.json`](../examples/workflows/refute.json) adds a fresh Artisan after plan and before acceptance; it does not change the shipped default.
Configure its `researcher`, `planner`, `reviewer` and `builder` profiles before selecting it as a repository's workflow; the refuter uses `reviewer` but may select any configured Artisan profile, including an optional role override.
The baseline Artisan role and [refutation stage guide](../baseline/stages/refute.md) supply the independent critic instructions without requiring a separate runtime or actor.
The [registry](../internal/workflow/registry.go) defines the typed ports; the example wires `plan.spec` and research notes into refutation, then `refute.findings` alongside `plan.spec` into acceptance.
See [the interpreter contract](interpreter.md#plan-refutation) for how advisory findings are presented and how acceptance remains bound to the spec.
The example's existing bounded `changes_requested` rule returns to plan and then runs a fresh refutation before asking again.
Trial this workflow on a real task before proposing it as the default; no real-task trial or model evaluation is claimed here.

### Choosing actors per repository

A repository changes who fulfils a step with `workflow_overrides` in the config, for example the Guildmaster instead of an Artisan for review:

```toml
[repository.workflow_overrides.review]
actor = "guildmaster"
```

Only a step whose kind allows more than one actor can be overridden, and only with an actor its kind allows, so the kind, wiring and other settings stay the same.
`workflow.ApplyOverrides` returns a copy with the requested `actor` settings replaced; callers must run `workflow.Validate` on the result before use.
The resulting definition's digest changes when its canonical content changes.

### Revisions

A workflow's digest is the sha256 of the parsed definition's canonical JSON encoding, written `sha256:<hex>`.
Reformatting the file or changing an omitted optional field to its empty value does not change the digest; changes retained in the canonical encoding do.
`workflow.Revision` supplies the name and digest pair used by [task acceptance](tasks.md#the-bound-workflow).

### Review outcome

Every `review` and `document-review` step produces the same outcome, schema `guildhall.review-outcome/2`, whoever its actor.
Every writer writes /2; readers still accept `guildhall.review-outcome/1` ([decision](decisions/review-outcome-2.md)).

| Field | Meaning |
|---|---|
| `reviewer` | `human` or `automated`. |
| `verdict` | `approved`, `changes_requested` or `rejected`; the latter two need at least one finding. An `approved` outcome may still carry findings, as a loop-exit accept does. |
| `summary` | Required: the reviewer's one-paragraph overall verdict, not blank, with no blank line, at most 64 KiB. |
| `findings[]` | The blockers: `id`, `rule` and `summary` (required), optional `file` and `line`. An `id` is the reviewer's stable name for the finding across rounds; `rule` cites the rule it breaks, and a finding without one is refused, with a message to move it to `suggestions`. |
| `suggestions[]` | The non-blocking comments: `id` and `summary` (required), optional `file` and `line`, no `rule`. A suggestion never blocks, never counts for `nothing_open` and needs no disposition in a later round. Ids are used once across `findings` and `suggestions`. |
| `dispositions[]` | What became of each finding of the step's previous outcome: `finding` (its id), `state` and `reason`. The state is `resolved` (listed in neither list), `unresolved` (listed again in `findings` under the same id) or `disputed` (listed again; the builder argued against it and the reviewer still holds it). `reason` is required for `disputed` and optional otherwise. |
| `revisions.subject` | What was reviewed: exactly one of `code_head`, the full commit id, or `document_digest`, the document's `sha256:<hex>` digest. |
| `revisions.spec_digest` | `sha256:<hex>` digest of the specification the build was given, the accepted one where the workflow has an acceptance step; required for a code head, optional for a document. |
| `revisions.standards_revision` | Central standards revision reviewed against; `""` means no standards revision is configured; nonempty whitespace-only values are invalid. |

An outcome blocks the task exactly when its verdict is `changes_requested`, whatever its findings cite; a reviewer with suggestions only records `approved` and lists them under `suggestions` ([decision](decisions/review-verdict-routes.md)).
A /1 outcome is read with the /2 meaning, in memory only: a finding without a rule is a suggestion, `still-open` is `unresolved` and `withdrawn` is `resolved`; its schema stays /1, it has no summary, and a /2-only key in it is refused.
A /1 `changes_requested` outcome whose findings all became suggestions still blocks.
An outcome counts only for the exact subject, specification digest and standards revision it names, so a document review never counts for a code head or the reverse.

### Workflow-edit authority

Directors may propose workflow changes.
Guildmaster approval is required before applying changes that affect review requirements, required checks or approval steps.
No blanket delegated authority or automatic self-approval is granted.
A task keeps its pinned workflow revision when a workflow is edited or the repository's `workflow` setting changes: it resolves its workflow by the bound name, and an edit that changes the bound revision's digest refuses its next advance rather than changing it.
An upgrade that changes the shipped `default` or `bug` does not strand a task bound to the earlier shipped revision, which stays resolvable for that task only and is never admitted for a new one ([decision](decisions/retired-shipped-revisions.md)).
Approvals are recorded per Director, against a definition's exact digest:

```sh
guildhall workflow approve --store director.store --file workflows/human-review.json --words-file -
guildhall workflow approve --store director.store --config director.toml --repo lib --words-file -
guildhall workflow approve --store director.store --config director.toml --repo lib --kind bug --words-file -
guildhall workflow approvals --store director.store --json
```

`approve` parses and validates the file, computes its digest and records the Guildmaster's words from the file or standard input, exactly as given; it prints the [store receipt](store.md#mutations-and-receipts).
With `--config` and `--repo` instead of `--file`, it approves that repository's effective workflow for the [task kind](tasks.md#task-kinds) `--kind` names, `feature` when it is not given: its definition with the repository's `workflow_overrides` applied and no role profile written in, resolved exactly as [`task new`](tasks.md#the-bound-workflow) resolves it; the approval carries the workflow's name and the effective digest.
`--kind` is a usage error without `--config` and `--repo`.
A refused definition, empty words or a missing store write nothing.
Repeating an approval prints the original receipt; different words for an approved digest are refused.
An edited definition has a new digest and needs its own approval.
The definitions shipped with this release, the `default` and `bug` workflows, count as approved by the release: `approvals` lists them with source `release` before the recorded approvals (source `guildmaster`).
Explicitly approving shipped content records the Guildmaster's words like any other approval; that revision then appears with both sources.
Commands exit 0 on success, 1 when the definition, words or store refuse or fail, and 2 on a usage error; they never create a store.
Parsing and validation accept or refuse a definition only; they never activate it.

#### Enforcement at task creation

`task new` admits the repository's effective workflow for the task's kind only when one of these holds, and otherwise refuses the task and writes nothing:

1. its digest is a definition shipped with this release;
2. its digest has a Guildmaster approval in the Director's store;
3. it meets the review floor and weakens nothing in an authorized baseline: a shipped definition, or the repository's workflow file before its overrides when that file's own digest is approved.

The refusal names the effective digest, each review-floor problem or each baseline with what the revision weakens in it, and the `workflow approve --config --repo` command that approves it.
Approving the exact effective digest currently admits a weakening edit, including one below the floor.
The accepted [rails, not tracks](decisions/rails-not-tracks.md) direction requires final independent review and required validation regardless of admission or route; enforcing that invariant independently of workflow shape is follow-up work, not a guarantee of the current admission check.

The classifier, `workflow.Weakenings`, is conservative: anything it cannot prove harmless is weakening.
It matches steps by id and guards the steps that review, approve, validate, publish or deliver (`review`, `document-review`, `guildmaster-approval`, `validate`, `publish`, `deliver`).
For a guarded step of the baseline, these are weakening:

- removing it, or changing its kind or kind version;
- a weaker actor: the Guildmaster is stronger than an Artisan, so only Artisan to Guildmaster is not weakening;
- any change to its other settings, such as a reviewer's `instructions`, `profile` or `skills`, which are reviewer-facing;
- dropping an input reference it had;
- removing a loop rule, sending it to another step, or lowering its `max`.

Changing the relative order of any steps the baseline and the revision share is weakening too.
Adding steps, adding input references or loop rules, raising `max`, and changing steps that are not guarded, the name or the description are not.
The classifier compares definitions only: the contents of files that a profile or skill name points to are not part of the workflow digest, so it cannot see them.
Nor are the role profile a step without a `profile` runs under and the settings of a profile, shipped or declared: only a `profile` written into a step is ([decision](decisions/role-profiles-outside-the-digest.md)).

The review floor, `workflow.FloorProblems`, is one non-author review and one validation before delivery: every `deliver` step must take a `review` step's outcome as one of its `review` inputs and a `validate` step's result as its `validation` input.
A `review` step is non-author by construction, because its actor is a fresh Artisan session or the Guildmaster, never the session that built the change.
A workflow with no `deliver` step delivers nothing and meets the floor.
The floor is fixed by this release; a managed policy key that sets it per repository is not built.
