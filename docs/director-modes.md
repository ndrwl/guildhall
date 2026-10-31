# Standalone and managed Directors

This page is partly built: [what already holds](#what-already-holds) says what is, and [what is missing](#what-is-missing) what is not.
Later slices build the rest red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [optional Steward](decisions/optional-steward.md).

A managed revision is authorized only by `guildhall config reload` or by the Guildmaster's approval of its alert decision; enrolment never authorizes one.
So an enrolled copy refuses every command that needs effective settings until a rollout or a hand-run `config reload` authorizes its `managed.toml`, and a hand edit of `managed.toml` is refused and raised to the Guildmaster as an [observed-untrusted alert](#observed-untrusted-alerts).

A Director runs in one of two modes:

- **standalone**: on its local config alone, with no Steward anywhere;
- **managed**: enrolled with a Steward, which publishes `managed.toml` to it by [rollout](steward.md).

The standing rule is that no Director feature requires a Steward.
Every Director feature works standalone; managed mode only adds a second source of settings and a place to send lessons.
The Steward stays optional for the whole guild: a Guildmaster with one machine and one Director never needs one.

## What already holds

On `main` today:

- `config.Load` falls back to `config.Parse` when no sibling `managed.toml` exists, so a copy with only `config.toml` loads ([configuration](configuration.md)).
- Repositories, their paths, owner machine, merge requirements and workflow, and the Artisan profiles are all in `config.toml`, so a standalone copy already holds its own guard rails for repositories and merging.
- `Config.Setting` gives a local value only where the managed key allows it, and a local value for a non-overridable managed key is refused, so managed keys win.
- Rollouts write only `managed.toml` and never `config.toml` ([managed publication](decisions/managed-publication-separate-from-director-config.md)), and refuse a copy whose repositories differ from the roster.
- `guildhall task new` loads the combined config and binds its digest to the task, so a changed managed revision reaches new tasks only (R1.62).
- The [Steward session](steward.md#the-steward-session) runs, supervised like a Director, with its own copy, store and Guildmaster-only inbox; it rolls out on the Guildmaster's word, and nothing a Director does depends on it.
- The mode is a store fact: the [enrolment and managed-revision events](store.md#managed-mode) are recorded, and the [mode table](#mode) decides the mode.
- Every Director command, and every Artisan attempt launched from a Director copy, loads its config through the shared configuration-admission boundary below (`internal/admit`), which refuses inline `[managed]` in every mode and refuses an enrolled copy whose `managed.toml` digest its store has not authorized; the refusing callers record that digest once as [observed-untrusted](#observed-untrusted-alerts) and raise its alert decision.
  These refuse: the session's start and reload, `task new`, `advance` and the commands that run it, `task cancel` and `task reconcile`, `task override set`, `finish`, `quality`, `ci log`, the workflow and grant commands that load a repository, `director leave`, and an attempt's launch, whose profile grant and Claude skill policy come from the admitted config.
  Clerk answers without a refused config and says on standard error that it was refused.
  These only report: `director status`, `director mode`, the messaging commands, `prune` and `version`.
  A manual `prune` acts on settings but never refuses: when the boundary refuses the copy, it applies `config.toml` alone, with no managed settings, and says so, so an unauthorized `prune_after` or `lock_wait` never decides what it removes.
- `guildhall director enrol`, `director leave` and `director mode` are built ([CLI contract](contracts/cli.md#director-modes)), and `director status` gives the [mode fields](#status-feed) the boundary finds.
- `guildhall config reload --copy <dir>` is built ([CLI contract](contracts/cli.md#config-reload)): it validates the managed copy's combined config and records a `managed-revision` event with source `reload`, the digest and the previous one, once per digest.
  A rollout's apply runs it over SSH on each copy it wrote, as one of the Steward's fixed [remote commands](steward.md#remote-commands), with the roster's per-machine `guildhall_path`, and records it in the receipt.
- [Observed-untrusted alerts](#observed-untrusted-alerts) are built: an unauthorized digest is recorded once, raised as an approve/reject decision in `needs_me`, and authorized by the Guildmaster's approval.
- `steward status` reads each roster copy's mode and managed digest with the fixed, read-only `director mode --json` over SSH ([Steward status](steward.md#status)).
- The Director suites in `internal/cli` run in both modes: `go test ./internal/cli -director-mode=managed`, or `GUILDHALL_TEST_DIRECTOR_MODE=managed`, makes each copy the shared test helpers return a managed copy with the same effective settings.

## What is missing

- Managed settings are flat string keys, so repositories and merge requirements cannot be managed today: they are local in both modes, and a Director copy's own files are the only record of them.

## Mode

The mode is a store fact, not a config key: the newest enrolment event in the Director's store decides it.

| Store says | `managed.toml` | Mode |
|---|---|---|
| No enrolment event | absent | standalone |
| No enrolment event | present | refused: run `guildhall director enrol --steward <label>` |
| Enrolled | present | managed |
| Enrolled | absent | refused: the managed settings are missing |
| Left | absent | standalone |
| Left | present | refused: a stale or unexpected managed file |

Every Director command uses one shared configuration-admission boundary: reject legacy inline settings, load and validate the files, check the mode, then check revision trust before applying settings.
Pure parsing APIs stay unchanged.
A refusal names the state and the command that fixes it.

Guildhall is pre-release: an inline `[managed]` section in `config.toml` is refused in every mode, whether or not a sibling managed file exists, with guidance to move it to `managed.toml` using the checksum-bearing format.
Migration then requires explicit enrolment and revision authorization; there is no unrecorded compatibility mode.

## Guard rails

The binary enforces the same guard rails in both modes; only where their values come from differs.

| Guard rail | Standalone | Managed |
|---|---|---|
| Allowed repositories | `[[repository]]` in `config.toml` | The same, checked against the roster at each rollout |
| Merge rule | `[repository.merge.requires]` per repository | The same; a future managed key may only turn requirements on |
| Attempt caps | `max_running_attempts` and `max_attempts_per_day` under `[local]` | Managed keys, read-only to the Director |

The binary's enforcement is described in [the Director's guard rails](director.md#guard-rails).

In managed mode the Director binary never writes `managed.toml`, and no Director command sets a managed key.
Each managed revision the binary applies is recorded with its digest.
At the shared configuration-admission boundary, a digest without a reload or explicit Guildmaster approval authorization recorded in the receiving store is recorded as `observed-untrusted` and raises a system alert for the Guildmaster.
A checksum or a receipt held only on the Steward is not authorization.
An observed-untrusted revision is not applied until the recorded reload command authorizes that exact digest or the Guildmaster explicitly approves that digest through the alert decision.
Observation and enrolment alone never authorize it.
Commands needing effective settings, including new-task creation and leave, refuse while the on-disk revision is untrusted; reload, approval and enrolment can validate it without applying it.
Active tasks retain their bound configuration.

### Observed-untrusted alerts

The first command that refuses an unauthorized digest records it: one `observed-untrusted` revision event per digest and enrolment, naming the digest applied before, and one `approval` decision `untrusted-<enrolment>-<first 12 hex digits>` with the options `approve` and `reject`, bound to the exact digest.
Later refusals of the same digest record nothing more.
The commands that only report, such as `director status` and `director mode`, record nothing.

The decision lives on the reserved task `managed-mode/untrusted` of the Director's own store, following the Steward store's reserved tasks.
Config validation refuses `managed-mode` as a real repository id, `task list`, the status feed's tasks and `prune` leave the task out, and its open decision reaches `needs_me` as any decision does.

- Answering `approve` with `guildhall decide answer` records a `guildmaster-approved` revision event naming the decision and the exact digest, which admits it; the Director answers `approve` only with the Guildmaster's own words.
  If that answer was recorded but its revision event was not, the next refusing command records it.
- Answering `reject` records no authorization: the refusal stays, naming the rejection, until `config reload` authorizes the digest or `managed.toml` is replaced with an authorized revision.
- `config reload` of the digest voids its decision if it still waits, and `director leave` voids every waiting decision of the enrolment, so nothing waits for them.

Decisions are per enrolment: enrolling again starts with no answered alert.

## Enrol and leave

Both are explicit commands run on the Director's machine, by the Guildmaster or by the setup wizard (R1.46), and both are store events with receipts that the task timeline and status feed show.

- `guildhall director enrol --steward <label>` requires a valid `managed.toml` beside the config and records `enrolled` with the Steward's label and the managed digest.
  The Steward's roster entry is the other half of enrolment and is written on the Steward's side, as today.
- `guildhall director leave` records `left`, writes the current effective values of the managed keys into `[local]` so no guard rail silently loosens, and renames `managed.toml` to `managed.toml.rev-<first 16 hex digits of its sha256>`, keeping it as a rollout keeps a revision.
  The Guildmaster removes the copy from the roster separately.

Both are idempotent through their mutation ids, and neither changes a task: tasks keep the configuration digest they bound.

## How the Steward acts

The Steward never messages a Director ([Director messaging](messaging.md#who-may-send)); it acts by command over SSH:

1. it copies `managed.toml` as [rollouts](steward.md#apply) do today;
2. it then runs `guildhall config reload --copy <dir>` on the copy with a fixed argument list.

`config reload` loads and validates the combined config, runs the migration and mode checks, and records a `managed-revision` event authorizing the rollout digest in the receiving store, with the new and previous digests and the source `reload`.
A running Director applies the new revision at its next step boundary, for new tasks only (R1.31, R1.62); an active task keeps its bound configuration.
If no reload runs and the receiving store has no authorization for the digest, the binary records it on first load as `observed-untrusted`, raises the alert and refuses to apply it.
Explicit Guildmaster approval records a revision authorization with source `guildmaster-approved`; only that approval or a recorded reload permits the observed revision to be applied.
Restarting the Director's own session is `guildhall director reload`, built as [the Director](director.md#reload) describes; moving it onto a new release is designed in [release upgrades](releases.md#guildhall-director-reload).

## Lessons

Lessons are recorded the same way in both modes: `guildhall lesson propose` stores a lesson document with its evidence and limits in the Director's store (R1.32).

- Standalone, the lesson stays local and the Guildmaster decides whether to adopt it into that Director's own config.
- Managed, the Steward collects proposed lessons over SSH on request, drafts the change and the Guildmaster approves it (R1.32).
  The Director never connects to the Steward, so Director machines still need no access to the private repository (R1.8).

Lessons are never broadcast from one Director to others.

## Status feed

`guildhall director status` shows the copy's mode in its `mode` object: `name` (`standalone`, `managed` or `refused`), any `refusal`, the Steward's label when enrolled (`steward`), the applied managed digest with when and by which source it was authorized (`applied_digest`, `applied_at`, `applied_by`), and any `observed-untrusted` digest awaiting authorization (`untrusted`: its digest, when it was observed, its alert decision and whether that `waiting`, was `rejected`, or was `approved` and not yet recorded).
Its messages section is the [mailbox summary](messaging.md#waking-a-director).

`guildhall steward status` shows each roster copy's mode, Steward label, managed digest, whether that copy's store authorizes it, any refusal and any untrusted digest, read with `director mode --json` over SSH; a copy it cannot read, such as on an unreachable machine, is that copy's problem ([Steward status](steward.md#status)).

## Testing both ways

Every Director suite that loads config runs twice, through one shared helper: once on a standalone copy and once on a managed copy whose `managed.toml` produces the same effective settings.
Both runs must give the same results; mode-specific behaviour, such as the mode table, enrol and leave, is tested separately.
CI makes no model calls and uses no SSH: the Steward's commands are replaced by the test binary acting as the program, as rollout tests replace `scp` today.

## Commands and store records

| Command | Does |
|---|---|
| `guildhall director enrol --steward <label>` | Records enrolment with the managed digest. |
| `guildhall director leave` | Records leaving, moves managed values to `[local]` and keeps `managed.toml` as a revision. |
| `guildhall director mode` | Prints the mode, Steward label, managed digest and the last revision event. |
| `guildhall config reload --copy <dir>` | Validates and records the published managed revision; run by the Steward over SSH or by hand. |
| `guildhall lesson propose <lesson-file\|->` | Records a lesson for local adoption or Steward collection. |

| Store record | Contract |
|---|---|
| Enrolment event | `enrolled` with the Steward label and managed digest, or `left` with the preserved revision name; ordered by sequence, never changed. |
| Managed revision event | New and previous digest, source `reload`, `observed-untrusted` or `guildmaster-approved`, and the time; never changed. A rollout authorizes through the `reload` it runs, since the Steward never writes a Director's store. Authorization must be recorded in the receiving store; observation records no authorization. Approval names the alert decision and exact approved digest. |
| Lesson | A document reference with evidence and limits, and a state `proposed`, `collected`, `adopted` or `declined`. |

## Conflicts resolved

These were flagged rather than chosen silently, and the Guildmaster resolved each as proposed here; [requirements](requirements.md) now says so:

- **R1.9** lists the copy layout with `mode` meaning steward or director and a `managed` section; the management mode here is a store fact so that `mode` keeps its meaning.
- **R1.46** and **R1.43** make enrolment and installation the Steward's; standalone Directors install without enrolment, such as by running the setup wizard locally (R1.51).
- **R1.39** and **R1.40** let the Herdr setup helper touch only machines enrolled by the Steward; a standalone Director's machine gets its own entry.
- **R1.32** has the Steward draft every lesson; a standalone lesson is adopted without one.

The [optional Steward decision](decisions/optional-steward.md) amends the [scp rollouts](decisions/steward-rollouts-copy-with-scp.md) no-remote-command restriction for `config reload`.
