# Standalone and managed Directors

Everything on this page is designed, not built, except where [what already holds](#what-already-holds) says otherwise.
No command, config key or store record it names exists yet; later slices build each contract red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [optional Steward](decisions/optional-steward.md).

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

## What is missing

- Nothing records which mode a copy is in; the mode is implied by whether `managed.toml` exists.
- The config key `mode` already means the copy's role, `steward` or `director` (R1.9), so the management mode cannot reuse it.
- `config.Parse` still accepts legacy inline `[managed]` settings in `config.toml`; in a standalone copy no Steward owns them.
- Managed settings are flat string keys, so repositories and merge requirements cannot be managed today: they are local in both modes, and a Director copy's own files are the only record of them.
- Nothing stops a local edit of `managed.toml`: its checksum line detects a damaged transfer, not who wrote it.
- There are no enrol or leave commands, no store events for them and no status feed.
- Tests load config one way per case; nothing runs a suite in both modes.

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
At the shared configuration-admission boundary, a digest without a rollout, reload or explicit Guildmaster approval authorization recorded in the receiving store is recorded as `observed-untrusted` and raises a system alert for the Guildmaster.
A checksum or a receipt held only on the Steward is not authorization.
An observed-untrusted revision is not applied until the recorded reload command authorizes that exact digest or the Guildmaster explicitly approves that digest through the alert decision.
Observation and enrolment alone never authorize it.
Commands needing effective settings, including new-task creation and leave, refuse while the on-disk revision is untrusted; reload, approval and enrolment can validate it without applying it.
Active tasks retain their bound configuration.

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

The planned status feed shows each Director's mode (`standalone` or `managed`), any mode refusal, the Steward's label when managed, the applied managed digest and when it was applied, any `observed-untrusted` digest awaiting authorization, and the [mailbox summary](messaging.md#waking-a-director).

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
| Managed revision event | New and previous digest, source `rollout`, `reload`, `observed-untrusted` or `guildmaster-approved`, and the time; never changed. Authorization must be recorded in the receiving store; observation records no authorization. Approval names the alert decision and exact approved digest. |
| Lesson | A document reference with evidence and limits, and a state `proposed`, `collected`, `adopted` or `declined`. |

## Conflicts to resolve

These are flagged rather than chosen silently:

- **R1.9** lists the copy layout with `mode` meaning steward or director and a `managed` section; the management mode here is a store fact so that `mode` keeps its meaning.
- **R1.46** and **R1.43** make enrolment and installation the Steward's; standalone Directors need a Steward-free install path, such as running the setup wizard locally (R1.51) without enrolment.
- **R1.39** and **R1.40** let the Herdr setup helper touch only machines enrolled by the Steward; a standalone Director's machine would need its own entry.
- **R1.32** has the Steward draft every lesson; a standalone lesson is adopted without one.

The [optional Steward decision](decisions/optional-steward.md) amends the [scp rollouts](decisions/steward-rollouts-copy-with-scp.md) no-remote-command restriction for `config reload`.
