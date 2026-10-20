# Steward rollouts

The Steward rolls out managed settings to enrolled Director copies over SSH (R1.7, R1.8, R1.31).
This page describes what `guildhall steward rollout` does today and what is planned.

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

Apply first refuses a requested machine set that differs from the saved plan, before any transport operation.
It plans again and refuses unless the result has the same digest as the dry run and the staged previews are unchanged, so a change observed during revalidation stops it.
Validated managed inputs are copied into private temporary input files for transport; receipts record those actual command paths rather than the preview source paths.
Local changes after revalidation remain untouched; readers validate overrides against the currently published managed settings.
For each copy that changes, it then runs the three planned commands:

1. copy the previous managed file to `managed.toml.rev-<first 16 hex digits of its sha256>` next to it, keeping the old revision;
2. copy the new managed file over `managed.toml`;
3. read `managed.toml` back and compare its sha256 with the plan.

A failure on one copy does not stop the others.
The receipt `receipt-<UTC time>.json` in the plan directory records each copy's status (`applied`, `unchanged`, `failed` or `verify_failed`), its before, after and read-back digests, the revision file and every command with its error.
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
Each `scp` run is bounded by a two-minute timeout.

## Revisions and active tasks

A managed revision is the sha256 of its `managed.toml`, which the plan and receipt record; plans separately bind the fetched local config digest.
Future task records must retain both local configuration and managed revision, not treat the managed digest alone as the entire effective config.
A rollout keeps each previous revision on the machine and never deletes one.
The [store](store.md) provides task records, but Director execution does not yet bind tasks to configuration revisions.
The intended consumer contract (R1.31) is that a task records the config revision and release in force when it is accepted and keeps them, while only new tasks pick up a rollout; that enforcement belongs to the Director's task store and execution, which are later work.

## Not implemented yet

- The release pin and side-by-side release installs (R1.31).
- `rollback`, which will restore a preserved revision for new tasks.
- Tool installs (`guildhall steward install`, R1.7, R1.50), audits and learnings (R1.32).
- Enrolment through the setup wizard (R1.43-R1.51).
- Platform support: tests replace `scp` with a stub on Linux, Windows and macOS, and no platform is claimed as supported for rollouts until acceptance passes there (R1.27).
