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
- Repositories, their paths, owner machine, merge policy and workflow, and the Artisan profiles are all in `config.toml`, so a standalone copy already holds its own guard rails for repositories and merging.
- `Config.Setting` gives a local value only where the managed key allows it, and a local value for a non-overridable managed key is refused, so managed keys win.
- Rollouts write only `managed.toml` and never `config.toml` (D14), and refuse a copy whose repositories differ from the roster.
- `guildhall task new` loads the combined config and binds its digest to the task, so a changed managed revision reaches new tasks only (R1.62).

## What is missing

- Nothing records which mode a copy is in; the mode is implied by whether `managed.toml` exists.
- The config key `mode` already means the copy's role, `steward` or `director` (R1.9), so the management mode cannot reuse it.
- `config.Parse` still accepts legacy inline `[managed]` settings in `config.toml`; in a standalone copy no Steward owns them.
- Managed settings are flat string keys, so repositories and merge policy cannot be managed today: they are local in both modes, and a Director copy's own files are the only record of them.
- Nothing stops a local edit of `managed.toml`: its checksum line detects a damaged transfer, not who wrote it.
- There are no spend limits.
- There are no enrol or leave commands, no store events for them and no status feed.
- Tests load config one way per case; nothing runs a suite in both modes.

## Mode

The mode is a store fact, not a config key: the newest enrolment event in the Director's store decides it.

| Store says | `managed.toml` | Mode |
|---|---|---|
| No enrolment event | absent | standalone |
| No enrolment event | present | managed, reported as `managed (unrecorded)` so copies enrolled before this design keep working |
| Enrolled | present | managed |
| Enrolled | absent | refused: the managed settings are missing |
| Left | absent | standalone |
| Left | present | refused: a stale or unexpected managed file |

`config.Load` keeps its file rules; the mode check is a separate step every Director command runs after loading, so the pure loader stays unchanged.
A refusal names the state and the command that fixes it.

In standalone mode an inline `[managed]` section is refused with guidance to move its values to `[local]`, since no Steward owns them; the legacy layout is accepted only as `managed (unrecorded)`.

## Guard rails

The binary enforces the same guard rails in both modes; only where their values come from differs.

| Guard rail | Standalone | Managed |
|---|---|---|
| Allowed repositories | `[[repository]]` in `config.toml` | The same, checked against the roster at each rollout |
| Merge rule | `merge_policy` per repository | The same; a future managed key may only tighten it to `approval` |
| Spend limits | `spend_limit_daily` and `spend_limit_task` in `[local]` | The same keys as managed settings, which win |

Spend limits do not cap work (R1.34): reaching one stops new Artisan launches and opens a Guildmaster decision, and never stops a running attempt.
Their unit is what the runtime adapter can report; the fake adapter reports none, so the limits are unproven until an adapter reports spend (R1.24).

In managed mode the Director binary never writes `managed.toml`, and no Director command sets a managed key.
Each managed revision the binary applies is recorded with its digest; a `managed.toml` whose digest matches no recorded rollout or reload is reported as an unexpected revision and raises a system alert for the Guildmaster, rather than being trusted or ignored.

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

`config reload` loads and validates the combined config, runs the mode check, and records a `managed-revision` event with the new and previous digests and the source `reload`.
A running Director applies the new revision at its next step boundary, for new tasks only (R1.31, R1.62); an active task keeps its bound configuration.
If no reload runs, the binary records the revision itself the first time it loads it, with the source `observed`, so every applied revision is a store event either way.
A restart command is not designed here; it belongs with the Steward's other features.

## Lessons

Lessons are recorded the same way in both modes: `guildhall lesson propose` stores a lesson document with its evidence and limits in the Director's store (R1.32).

- Standalone, the lesson stays local and the Guildmaster decides whether to adopt it into that Director's own config.
- Managed, the Steward collects proposed lessons over SSH on request, drafts the change and the Guildmaster approves it (R1.32).
  The Director never connects to the Steward, so Director machines still need no access to the private repository (R1.8).

Lessons are never broadcast from one Director to others.

## Status feed

The planned status feed shows each Director's mode (`standalone`, `managed` or `managed (unrecorded)`), the Steward's label when managed, the current managed digest and when it was applied, and the [mailbox summary](messaging.md#waking-a-director).

## Testing both ways

Every Director suite that loads config runs twice, through one shared helper: once on a standalone copy and once on a managed copy whose `managed.toml` produces the same effective settings.
Both runs must give the same results; mode-specific behaviour, such as the mode table, enrol and leave, is tested separately.
CI makes no model calls and uses no SSH: the Steward's commands are replaced by the test binary acting as the program, as rollout tests replace `scp` today.

## Commands, config keys and store records

| Command | Does |
|---|---|
| `guildhall director enrol --steward <label>` | Records enrolment with the managed digest. |
| `guildhall director leave` | Records leaving, moves managed values to `[local]` and keeps `managed.toml` as a revision. |
| `guildhall director mode` | Prints the mode, Steward label, managed digest and the last revision event. |
| `guildhall config reload --copy <dir>` | Validates and records the published managed revision; run by the Steward over SSH or by hand. |
| `guildhall lesson propose <lesson-file\|->` | Records a lesson for local adoption or Steward collection. |

| Config key | Meaning |
|---|---|
| `spend_limit_daily`, `spend_limit_task` | Spend limits in the adapter's reported unit; unset means none. |

| Store record | Contract |
|---|---|
| Enrolment event | `enrolled` with the Steward label and managed digest, or `left` with the preserved revision name; ordered by sequence, never changed. |
| Managed revision event | New and previous digest, source `reload`, `observed` or `unexpected`, and the time; never changed. |
| Lesson | A document reference with evidence and limits, and a state `proposed`, `collected`, `adopted` or `declined`. |

## Conflicts to resolve

These are flagged rather than chosen silently:

- **R1.9** lists the copy layout with `mode` meaning steward or director and a `managed` section; the management mode here is a store fact so that `mode` keeps its meaning.
- **R1.46** and **R1.43** make enrolment and installation the Steward's; standalone Directors need a Steward-free install path, such as running the setup wizard locally (R1.51) without enrolment.
- **R1.39** and **R1.40** let the Herdr setup helper touch only machines enrolled by the Steward; a standalone Director's machine would need its own entry.
- **R1.32** has the Steward draft every lesson; a standalone lesson is adopted without one.
- **R1.34** says work is not capped; spend limits here pause new launches for a decision instead of capping work.
- **D12** says rollouts copy files with `scp` and run no remote command; `config reload` over SSH is a remote command and needs a decision superseding that part of D12.
