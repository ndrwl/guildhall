# The Steward

The Steward looks after the guild's Director copies across its machines: it owns the roster and rolls out managed settings to enrolled Director copies over SSH (R1.7, R1.8, R1.31).
It runs as the **Steward session**, a long-running agent session the Guildhall binary starts and supervises the same way it supervises a Director, and only the Guildmaster talks to it.
This page describes the Steward session, what `guildhall steward rollout` does today and what is planned.

No Director feature requires a Steward.
A standalone copy, one with no `managed.toml` and no enrolment, starts and behaves exactly as it does without one ([standalone and managed Directors](director-modes.md)).

## The Steward session

The Steward session is not a service or daemon (R1.29): it is an agent session in a Herdr tab, like a [Director session](director.md#start), that runs while the Guildmaster keeps it running.
It only orchestrates.
Every change it makes goes through a fixed `guildhall steward` command, and the Guildmaster approves every policy change and rollout digest ([approval in a Steward session](#approval-in-a-steward-session)).
It never messages a Director, and no Director reaches it: it pushes only by rollout (R1.6, R1.8; [messaging](messaging.md#recipients)).

### The Steward copy

A Steward copy is one self-contained directory:

| Path | What it is |
|---|---|
| `config.toml` | The Steward config: `mode = "steward"`, `[private_config]` and `[steward]`. |
| `steward.store` | The Steward's store: its session records, notes, reloads, handoff triggers, inbox and rollout approvals, kept by the same store code as a `director.store`. Create it with `guildhall store init <dir>/steward.store`. |
| `rollouts/` | One directory per rollout plan, where [`steward status`](#status) reads each copy's last receipt. |
| `director/session-<n>/` | Each session's rendered role, startup and runtime files, as for a Director. |
| the private config checkout | The checkout of the private config repository, such as `ndrwl/guildhall-config`, that `[private_config] location` names, holding [`roster.toml`](#rostertoml) and [`managed.toml`](#managedtoml). |

Copy discovery finds a Steward copy as it finds a Director's, by its copy marker, and a copy that holds `steward.store` and no `director.store` is a Steward copy, whose store every command defaults to ([copies](running.md#copies)).
The session's own hooks, such as `guildhall director turn-ended` and `guildhall director note add`, therefore work in a Steward copy unchanged and act on its `steward.store`.
Keep the Steward copy's `config.toml` outside the private checkout, so `config.Load` does not treat the private `managed.toml` as its published sibling.

### Config

```toml
schema_version = 1
mode = "steward"

[private_config]
location = "/home/guild/steward/guildhall-config"

[steward]
name = "steward"
adapter = "claude"
model = "opus"
effort = "high"
```

`[steward]` is valid only in a Steward config, and `[director]` only in a Director's.
`name` labels the Steward's Herdr workspace and tab and is required to start a session; `adapter`, `model`, `effort` and `role` choose the session's runtime and role prompt as `[director]` does for a Director, and `role` is a config-relative path that replaces the release's `roles/steward.md`.
A config that only runs rollouts needs no `[steward]` table.
The table is an addition to schema version 1, so an older binary refuses it as an unknown key rather than guessing (R1.11).
Like every config, it names no credential: each machine's address, user and key stay in the Steward user's SSH config behind its roster `ssh_alias`.

### Start

```sh
guildhall steward start [runtime] [--tab] [--check] [--copy <dir>]
```

`steward start` runs the same supervisor as [`director start`](director.md#start), with the Steward's own preflight and steps.
Its preflight checks, in order: the config is a Steward config with a `[steward] name` and no `managed.toml` beside it, since a Steward copy is never managed; the runtime is known and on `PATH`; `guildhall` is on `PATH`; `steward.store` opens; the private config location is an absolute directory that is a Git work tree; its roster is valid; and `ssh` and `scp` are on `PATH`.
`--check` runs the preflight only, and `--tab` opens the tab `steward <name>` in the Herdr workspace named by `[steward] name`, as a Director's tab opens in its own.
Each session renders the release's `roles/steward.md`, or the copy's `baseline/roles/steward.md`, and every release skill, and its `startup.md` lists the live notes and the rollout approvals in flight: each one waiting for the Guildmaster and each one they approved.
A session starts `recovering` and starts nothing new until `guildhall steward ready`.
The Steward's session has no task steps: no workspace close sweep, prune, in-flight task snapshot, repository policies or task watchers.

### Lifecycle

```sh
guildhall steward ready|pause|resume [--copy <dir>]
guildhall steward stop [--now] [--copy <dir>]
guildhall steward reload [--now] [--copy <dir>]
guildhall steward handoff [--copy <dir>]
```

Each runs the Director's command of the same name, [ready](director.md#ready), [pause and resume](director.md#pause-and-resume), [stop](director.md#stop) and [reload](director.md#reload), on the Steward copy's `steward.store`, and refuses any copy whose store is not a `steward.store`.
`steward handoff` is `steward reload --now`: a fresh session picks up from this one, with every live note in its `startup.md`.
The automatic handoff triggers of a Director's session, such as the context trigger, apply to the Steward's too.

### Inbox

The Steward's inbox is the `steward` recipient of its store, and it takes messages from the Guildmaster only: `guildhall messages send steward <envelope>` run outside any Guildhall session or attempt ([senders](messaging.md#senders)).
A message to it from a Director, another Director or an Artisan is refused, and so is a `director` message to a Steward store or a `steward` message to any other store.
Its envelope names the reserved task `steward/guildmaster`.
A message wakes the Steward session as a Director's wakes a Director ([waking the Director](messaging.md#waking-the-director)).
The sender list is the one place a later Steward-to-Director or Director-to-Steward path would extend; none exists today.

### Status

```sh
guildhall steward status [--copy <dir>]
```

`steward status` prints one JSON document: the newest Steward session, every roster machine with its SSH alias, OS and Director copies, each copy's last rollout from the newest receipt under the copy's `rollouts/`, and every rollout approval with its state, `waiting`, `approved`, `declined` or `void`.
It reads local files only; nothing reaches a Director.

### Approval in a Steward session

A rollout plan that needs approval is applied with `--approve <plan digest>`, given only after the Guildmaster approves that exact plan ([approval](#approval)).
Inside a Guildhall session or attempt, the Steward's among them, `--approve` is refused unless the Steward store holds the Guildmaster's approval of that exact digest:

```sh
guildhall steward approval request --plan <dir> [--copy <dir>]
guildhall decide answer --copy <dir> --repo steward --task rollouts --decision rollout-<12 hex> --answer-file -
```

`steward approval request` opens, once, the approval decision `rollout-<first 12 hex digits of the digest>` of the reserved task `steward/rollouts`, bound to the plan digest, with the options `approve` and `decline`, and prints it with the command the Guildmaster answers it with.
Only the Guildmaster answers it, outside any session: `decide answer` refuses it from inside one.
An answer of `approve` lets the session apply that one plan with `--approve`; `decline`, or a plan with another digest, does not.
Outside any session, `--approve` stands on the Guildmaster's word as before, and the plan and apply digests, the lock, the receipts, the [managed publication](decisions/managed-publication-separate-from-director-config.md) rule and the refusal of changed copies are unchanged.

### Moving the Steward to another host

The Steward copy is self-contained, so moving it, such as from a laptop to a Linux VDS, needs no code change:

1. Stop the session with `guildhall steward stop` and wait for it to stop.
2. Confirm no rollout is applying: the private checkout holds no `.rollout.lock`, or its writer has stopped.
3. Copy the Steward copy, its `steward.store`, `rollouts/` and private config checkout, to the new host, and give the new host's Steward user the SSH config behind every roster `ssh_alias`.
4. Start the session on the new host with `guildhall steward start --copy <dir>`, and never again on the old one: there is one Steward writer per fleet.

## Enrolment comes first

A rollout only reaches machines and Director copies that are already in the roster.
Enrolment, creating the machine's Guildhall account, SSH setup, host-key trust, Claude and forge sign-in and administrator steps belong to the setup wizard (R1.43-R1.51), which is not implemented yet.
Until then the roster is written by hand in the private config directory.
A rollout never adds a machine, account, Director or repository to the roster, never creates a Director copy and never asks for or stores a credential.

Once a copy is enrolled, a routine rollout runs without repeating setup.
Changes outside routine authority need the Guildmaster's approval of the exact plan, as described under [approval](#approval).

## Private config directory

The Steward copy's config names the private config directory in `[private_config] location`.
For a rollout this must be an absolute local directory, such as a clone of the private repository (R1.3).
Two files in it drive rollouts; neither belongs in this public repository (R1.2).

### `roster.toml`

```toml
schema_version = 1

[[machine]]
label = "workstation"
ssh_alias = "guild-workstation"
os = "linux"
roles = ["steward", "director"]

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/directors/tools"
repositories = ["cli", "docs-site"]
```

| Key | Meaning |
|---|---|
| `schema_version` | Required; currently `1`. A newer version is refused, and unknown keys are refused. |
| `[[machine]] label` | The machine's unique lowercase name; repositories name it as `owner_machine`. |
| `ssh_alias` | A `Host` entry in the Steward user's SSH config. The alias, not the roster, holds the address, user and key. Letters, digits, `.`, `_` and `-` only, so no user, option or shell syntax can be passed. |
| `os` | `linux`, `windows` or `darwin`; it decides the path form. |
| `roles` | `steward` and/or `director`. A machine that lists Directors needs `director`. |
| `[[machine.director]] name` | The Director's name, which its copy's config must match. |
| `copy_dir` | Absolute path of the Director copy's directory, holding `config.toml` and the published `managed.toml`. Windows paths use a drive letter and forward slashes, such as `C:/Users/guild/directors/tools`. Only letters, digits, `.`, `_`, `-` and `/` are allowed, with no `.` or `..` segments. |
| `repositories` | The repository ids the copy must have. |

Each machine has one SSH alias for its one non-administrator Guildhall account, and that account may host several Directors, each with its own copy directory.

### `managed.toml`

```toml
schema_version = 1

[managed.standards_revision]
value = "standards-2"
policy = true

[managed.review_instructions]
value = "Cite the accepted requirement for every finding."
overridable = true
```

This private source is distinct from each copy's checksum-framed published file described in [configuration](configuration.md).
Keep the Steward copy's own config outside this private source directory so `config.Load` does not treat the source as its published sibling.
Each `[managed.<key>]` table has a `value` and `overridable` as in a copy's config, plus `policy`.
`policy = true` marks a key, such as a review or approval rule, whose every change needs approval; it is not written to copies.
Keys follow the config key rules, so credential-like names are refused.

## Dry run

```sh
guildhall steward rollout --config <steward.toml> --machine <label> [--machine <label>...] --plan <new dir> --dry-run
```

For each Director copy on each named machine, the dry run downloads `config.toml` and `managed.toml`, renders new managed settings and checks the combined config.
It writes `plan.json` and, for each copy that would change, `files/<machine>/<director>/previous.toml` and `managed.toml` previews into the new plan directory, and prints:

- each copy's status: `change`, `unchanged` or `refused` with the reason;
- each managed key added, changed or removed, and whether it needs approval;
- a line diff of the published managed file;
- the planned `scp` commands, with preview source paths replaced by private temporary inputs during [apply](#apply);
- the plan digest and the apply command.

It does not update remote Director copies; its writes are local plan files and temporary downloads.
It exits 1 if any copy was refused.

A copy is refused when it cannot be fetched, is not valid config, is not in director mode, names another Director, has repositories other than the roster lists, has a repository whose `owner_machine` is not the roster machine, or has a local value for a key the new managed section makes non-overridable.
A refused copy is reported, never overwritten, and a plan with a refused copy cannot be applied.

### What a rollout changes

Only the separate `managed.toml` is replaced; `config.toml` is never written, even when an editor changes it during apply.
During enrolment, move any inline managed settings out of `config.toml` into a checksum-framed `managed.toml` generated with `config.EncodeManaged`; preserve their existing values and override flags.
Rollouts require both files and refuse inline managed layouts.
New copies with no previous managed settings can start with [`examples/managed.toml`](../examples/managed.toml) beside their local config.
See [configuration](configuration.md) for loading APIs and legacy-layout compatibility.
A rollout never changes a local value: a new managed key that conflicts with one is refused, and the Director or Guildmaster resolves it.

## Apply

```sh
guildhall steward rollout --config <steward.toml> --machine <label> --plan <dir> --apply [--approve <plan digest>]
```

Apply reads `plan.json` as a `guildhall.rollout-plan/1` document using the [strict JSON decoding rules](contracts/strictjson.md), refusing malformed or newer plans before any transport operation.
It then refuses a requested machine set that differs from the saved plan, before any transport operation.
It plans again and refuses unless the result has the same digest as the dry run and the staged previews are unchanged, so a change observed during revalidation stops it.
Validated managed inputs are copied into private temporary input files for transport; receipts record those actual command paths rather than the preview source paths.
Local changes after revalidation remain untouched; readers validate overrides against the currently published managed settings.
For each copy that changes, it then runs the three planned commands:

1. copy the previous managed file to `managed.toml.rev-<first 16 hex digits of its sha256>` next to it, keeping the old revision;
2. copy the new managed file over `managed.toml`;
3. read `managed.toml` back and compare its sha256 with the plan.

A failure on one copy does not stop the others.
The receipt `receipt-<UTC time>.json` in the plan directory records each copy's status (`applied`, `unchanged`, `failed` or `verify_failed`), its before and after digests, its read-back digest when verification reaches digest comparison, the revision file and every attempted command with its error.
Read-back follows successful uploads only; failed transfers are recorded as failed and may leave partial remote files without a read-back digest.
Apply exits 1 unless every copy is applied or unchanged; a plan that is no longer current needs a new dry run.
Applying an unchanged plan performs no remote writes, but still writes a local receipt.

### Publication and writer containment

The [managed-file checksum framing](configuration.md#config-file) makes combined loading explicitly refuse incomplete or corrupted transfers, including syntactically valid truncated TOML.
This is integrity detection, not atomic publication: non-atomic scp may transiently refuse reads and does not prove uninterrupted config availability.
The `.rollout.lock` file in the private config directory excludes concurrent applies using that directory on this Steward.
It does not protect against another host, another private directory or a manual managed-file writer; designate one Steward writer for each fleet and do not edit managed files during rollout.
If a writer crashes, stop competing invocations and confirm that the owning process has exited before manually removing its stale lock.
Never remove a lock held by another live writer; there is no automatic stale-lock deletion.
Keep the plan directory private while applying.

### Approval

These changes are outside routine rollout authority and need `--approve` with the plan's exact digest, given only after the Guildmaster approves that plan:

- any change to a key marked `policy`;
- removing a managed key, which lets copies set it locally;
- making a managed key overridable.

Adding a key, changing a value and making a key non-overridable are routine.
An approval covers one plan digest only; any later change needs a new plan.
New machines, accounts, privileges, host-key trust and credentials are never part of a rollout.

## Transport and trust

Rollouts run `scp` from `PATH` with fixed argument lists, never through a local shell: `scp -q -B -o BatchMode=yes -- <source> <destination>`.
SSH's own config, agent, keys and host-key checking decide access; Guildhall adds no options that weaken them, and batch mode makes `scp` fail instead of prompting.
No remote command is run.
Roster values that reach the command line are restricted to characters that no remote shell or `scp` interprets.
Each `scp` run is bounded by a two-minute timeout; a run stopped by its timeout is reported as a timeout, and one that fails before transferring leaves its destination unchanged.

## Revisions and active tasks

A managed revision is the sha256 of its `managed.toml`, which the plan and receipt record; plans separately bind the fetched local config digest.
For task configuration binding, see [tasks](tasks.md#the-bound-workflow); the managed digest alone is not the entire effective config.
A rollout keeps each previous revision on the machine and never deletes one.
Release binding and execution enforcement of R1.31 remain future work.

## Not implemented yet

- The release pin and side-by-side release installs (R1.31), designed, not built, in [release upgrades](releases.md).
- `rollback`, which will restore a preserved revision for new tasks.
- Tool installs (`guildhall steward install`, R1.7, R1.50), audits and learnings (R1.32).
- Enrolment through the setup wizard (R1.43-R1.51).
- The Steward is optional: [standalone and managed Directors](director-modes.md), including `guildhall config reload` and recorded enrolment, are designed, not built.
- Platform support: tests replace `scp` with a stub on Linux, Windows and macOS, and no platform is claimed as supported for rollouts until acceptance passes there (R1.27).
- The Steward session's live acceptance: CI runs it against a stand-in runtime only, so it is not claimed until the Guildmaster has run it once with three Director hosts (R1.27, R1.30).
- Managed mode for the Steward session ([optional Steward](decisions/optional-steward.md)): enrolment, `config reload` over SSH after an approved rollout, observed-untrusted alerts and the mode fields in the status feeds, follow-up `guildhall/d102`.
- Collection of lessons and maintenance tickets from Directors, and their triage, follow-up `guildhall/d103`.
