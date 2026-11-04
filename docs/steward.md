# The Steward

The Steward looks after the guild's Director copies across its machines: it owns the roster and rolls out managed settings to enrolled Director copies over SSH (R1.7, R1.8, R1.31).
It runs as the **Steward session**, a long-running agent session the Guildhall binary starts and supervises the same way it supervises a Director, and only the Guildmaster talks to it: no Director reaches its inbox.
This page describes the Steward session, what `guildhall steward rollout`, `guildhall steward collect`, `guildhall steward takeover` and `guildhall steward secrets` do today and what is planned.

No Director feature requires a Steward.
A standalone copy, one with no `managed.toml` and no enrolment, starts and behaves exactly as it does without one ([standalone and managed Directors](director-modes.md)).

## The Steward session

The Steward session is not a service or daemon (R1.29): it is an agent session in a Herdr tab, like a [Director session](director.md#start), that runs while the Guildmaster keeps it running.
It only orchestrates.
Every change it makes goes through a fixed `guildhall steward` command, and the Guildmaster approves every policy change and rollout digest ([approval in a Steward session](#approval-in-a-steward-session)).
No Director reaches it: it pushes only by rollout, reads Directors' lessons and incidents only by [collection](#collection), and informs an enrolled Director of issues it finds only by a [Steward message](#steward-messages), sent by its supervisor and never by its model (R1.6, R1.8; [messaging](messaging.md#steward-messages)).

### The Steward copy

A Steward copy is one self-contained directory:

| Path | What it is |
|---|---|
| `config.toml` | The Steward config: `mode = "steward"`, `[private_config]` and `[steward]`. |
| `steward.store` | The Steward's store: its session records, notes, reloads, handoff triggers, inbox, rollout approvals and triage decisions, kept by the same store code as a `director.store`. Create it with `guildhall store init <dir>/steward.store`. |
| `rollouts/` | One directory per rollout plan, where [`steward status`](#status) reads each copy's last receipt. |
| `director/session-<n>/` | Each session's rendered role, startup and runtime files, as for a Director. |
| the private config checkout | The checkout of the private config repository, such as `ndrwl/guildhall-config`, that `[private_config] location` names, holding [`roster.toml`](#rostertoml), [`managed.toml`](#managedtoml) and the [collected](#collection) `lessons/` and `incidents/`. |

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
collect_every = "6h"
```

`[steward]` is valid only in a Steward config, and `[director]` only in a Director's.
`name` labels the Steward's Herdr workspace and tab and is required to start a session; `adapter`, `model`, `effort` and `role` choose the session's runtime and role prompt as `[director]` does for a Director, and `role` is a config-relative path that replaces the release's `roles/steward.md`.
`collect_every` is optional: a duration of at least a minute, validated like `[backup] every`, after which the session runs a [scheduled collection](#scheduled-collection); unset, the default, nothing is collected on a schedule.
`report_unreachable_after` is optional too: how long a copy must have been unreachable before a scheduled run, reaching it again, sends it an `unreachable-recovered` [Steward message](#steward-messages), a duration of at least a minute, one hour when unset.
`report_director_after` is optional as well: the span of the [Director health](#director-health) checks, how long a Director must have taken no turn while messages waited, stayed recovering, or run a release behind its pin before a scheduled run reports it, a duration of at least a minute, one hour when unset.
A config that only runs rollouts needs no `[steward]` table.
The table has no `idle_after`, handoff or `wake_quiet` key: the Steward's session runs the defaults of those ([lifecycle](#lifecycle)).
The table is an addition to schema version 1, so an older binary refuses it as an unknown key rather than guessing (R1.11).
Like every config, it names no credential: each machine's address, user and key stay in the Steward user's SSH config behind its roster `ssh_alias`.

### Start

```sh
guildhall steward start [runtime] [--tab] [--check] [--copy <dir>]
```

`steward start` runs the same supervisor as [`director start`](director.md#start), with the Steward's own preflight and steps.
Its preflight checks, in order: the config is a Steward config with a `[steward] name` and no `managed.toml` beside it, since a Steward copy is never managed; the runtime is known and on `PATH`; `guildhall` is on `PATH`; `steward.store` opens; the private config location is an absolute directory that is a Git work tree; its roster is valid; and `ssh` and `scp` are on `PATH`.
Like `director start`, it then decrypts the machine's [secrets file](secrets.md#at-start) in memory, withheld from the Steward's runtime.
`--check` runs the preflight only, then probes the [sign-in](signin.md#in-the-start-preflight) of the Steward copy's runtimes, report only, never `gh`; and `--tab` opens the tab `steward <name>` in the Herdr workspace named by `[steward] name`, as a Director's tab opens in its own.
Each session renders the release's `roles/steward.md`, or the copy's `baseline/roles/steward.md`, and every release skill, and its `startup.md` lists the live notes and the decisions in flight: each rollout approval waiting for the Guildmaster, each one they approved, and each open [triage](#triage) decision.
A session starts `recovering` and starts nothing new until `guildhall steward ready`.
The Steward's session has no task steps: no workspace close sweep, prune, in-flight task snapshot, repository policies or task watchers; its only scheduled work is the optional [scheduled collection](#scheduled-collection).

### Lifecycle

```sh
guildhall steward ready|pause|resume [--copy <dir>]
guildhall steward stop [--now] [--copy <dir>]
guildhall steward reload [--now] [--copy <dir>]
guildhall steward handoff [--copy <dir>]
```

Each runs the Director's command of the same name, [ready](director.md#ready), [pause and resume](director.md#pause-and-resume), [stop](director.md#stop) and [reload](director.md#reload), on the Steward copy's `steward.store`, and refuses any copy whose store is not a `steward.store`.
`steward handoff` is `steward reload --now`: a fresh session picks up from this one, with every live note in its `startup.md`.
The Steward's session runs the defaults of a Director's session settings: `idle_after` (5 minutes), the [automatic handoff](director.md#automatic-handoff) triggers (the context trigger at 70%, with the after-tasks and idle triggers off) and `wake_quiet` (3 seconds).
They cannot be changed: the first four are `[director]` keys, and a Steward config refuses that table and the same keys under `[steward]`, and it refuses `wake_quiet` in `[local]` or `[managed]`.
A wake line ending `; handoff due (<reason>)` is answered by the `guildhall-steward-handoff` skill, which runs `steward handoff`.

### Inbox

The Steward's inbox is the `steward` recipient of its store, and it takes messages from two senders ([senders](messaging.md#senders)):

- the Guildmaster: `guildhall messages send steward <envelope>` run outside any Guildhall session or attempt, whose envelope names the reserved task `steward/guildmaster`;
- `director`: the `triage-opened` report of the Steward's own [scheduled collection](#scheduled-collection), naming `steward/incidents` or `steward/lessons`, and its `lock-busy` report, naming `steward/rollouts`, sent by its supervisor and never by `messages send`.

`messages send` to it from a Director, another Director, an Artisan or the Steward's own session is refused, and so is a `director` message to a Steward store or a `steward` message to any other store.
No Director reaches the inbox: the `director` tag says the report is about items Directors filed, which the Steward read itself.
A message wakes the Steward session as a Director's wakes a Director ([waking the Director](messaging.md#waking-the-director)).
[Steward messages](#steward-messages) go the other way, from the Steward to a Director's external inbox, and add no sender to this list.

### Status

```sh
guildhall steward status [--copy <dir>]
```

`steward status` prints one JSON document: the newest Steward session, every roster machine with its SSH alias, OS and Director copies, each copy's last rollout from the newest receipt under the copy's `rollouts/`, and every rollout and setup plan approval with its state, `waiting`, `approved`, `declined` or `void`.
Each copy also has its `mode`, read with the fixed, read-only [remote command](#remote-commands) `director mode --json` over SSH: the copy's mode name, the Steward it is enrolled with, its `managed.toml` digest, whether the copy's own store authorizes that digest, any refusal, and any `untrusted` digest waiting for the Guildmaster's answer on that copy.
A copy whose mode cannot be read, because its machine is unreachable, `ssh` is missing or its `guildhall` is too old to know `director mode`, has no `mode`, a `problem` saying why, and a line in `problems`; the command still succeeds.
Nothing else reaches a Director, and status changes nothing.

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
A [takeover](#takeover) plan is approved the same way: `steward approval request --plan <dir>` sees it is a setup plan and opens `setup-<first 12 hex digits>` on the reserved task `steward/setup` instead, which `decide answer` likewise refuses from inside a session, and every takeover needs it.
A [secrets](#secrets) plan opens `secrets-<first 12 hex digits>` on the reserved task `steward/secrets` the same way, and every secrets push needs it.
Outside any session, `--approve` stands on the Guildmaster's word as before, and the plan and apply digests, the lock, the receipts, the [managed publication](decisions/managed-publication-separate-from-director-config.md) rule and the refusal of changed copies are unchanged.

### Moving the Steward to another host

The Steward copy is self-contained, so moving it, such as from a laptop to a Linux VDS, needs no code change:

1. Stop the session with `guildhall steward stop` and wait for it to stop.
2. Confirm no rollout is applying: the private checkout holds no `.rollout.lock`, or its writer has stopped.
3. Copy the Steward copy, its `steward.store`, `rollouts/` and private config checkout, to the new host, and give the new host's Steward user the SSH config behind every roster `ssh_alias`.
4. Start the session on the new host with `guildhall steward start --copy <dir>`, and never again on the old one: there is one Steward writer per fleet.

## Enrolment comes first

A rollout only reaches machines and Director copies that are already in the roster.
An existing Director copy is brought into the roster by a [takeover](#takeover), or by hand ([managing a Director copy](#managing-a-director-copy)).
Creating the machine's Guildhall account, SSH setup, host-key trust, Claude and forge sign-in and administrator steps belong to the setup wizard (R1.43-R1.51), which is not implemented yet; its sign-in step is built as the standalone [`guildhall signin`](signin.md), which checks, guides and verifies each runtime's sign-in on the machine it runs on.
The engine such a wizard and d128's provisioning run on, [`guildhall setup`](setup.md), is built: its doctor reports a machine's gaps against step files and its wizard walks a person through the human ones, but no Steward command runs it on a machine yet.
A rollout never adds a machine, account, Director or repository to the roster, never creates a Director copy and never asks for or stores a credential.
Each machine's other [secrets](secrets.md) are sops ciphertext encrypted to its own age key, from `guildhall secrets init`, and decrypted in memory by `director start` and `steward start`; the Steward sets and pushes them with [`steward secrets`](#secrets), a separate command from rollouts.

Once a copy is enrolled, a routine rollout runs without repeating setup.
Changes outside routine authority need the Guildmaster's approval of the exact plan, as described under [approval](#approval).

## Private config directory

The Steward copy's config names the private config directory in `[private_config] location`.
For a rollout this must be an absolute local directory, such as a clone of the private repository (R1.3).
Two files in it drive rollouts; neither belongs in this public repository (R1.2).

### `roster.toml`

```toml
schema_version = 1
recovery_recipient = "age1..."

[[machine]]
label = "workstation"
ssh_alias = "guild-workstation"
os = "linux"
roles = ["steward", "director"]
age_recipient = "age1..."

[[machine.director]]
name = "tools"
copy_dir = "/home/guild/directors/tools"
repositories = ["cli", "docs-site"]
```

| Key | Meaning |
|---|---|
| `schema_version` | Required; currently `1`. A newer version is refused, and unknown keys are refused. |
| `recovery_recipient` | Optional. The age public key of the Guildmaster's recovery key, which every machine's [secrets file](secrets.md#the-steward-pushes-them) is also encrypted to; [`steward secrets`](#secrets) needs it. Not a secret. |
| `[[machine]] label` | The machine's unique lowercase name; repositories name it as `owner_machine`. |
| `ssh_alias` | A `Host` entry in the Steward user's SSH config. The alias, not the roster, holds the address, user and key. Letters, digits, `.`, `_` and `-` only, so no user, option or shell syntax can be passed. |
| `os` | `linux`, `windows` or `darwin`; it decides the path form. |
| `roles` | `steward` and/or `director`. A machine that lists Directors needs `director`. |
| `guildhall_path` | Optional. The absolute path of the `guildhall` program the [remote commands](#remote-commands) run on the machine, in its `os` path form with the same characters as `copy_dir`, such as `C:/Users/guild/bin/guildhall.exe`. Without it, `guildhall` on the `PATH` of the machine's Guildhall account is run. |
| `age_recipient` | Optional. The public key `guildhall secrets init` printed on the machine, which its [secrets file](secrets.md#the-steward-pushes-them) is encrypted to; without it the machine gets no pushed secrets. Not a secret, and no two machines, nor the recovery key, share one. |
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

Rollout, like collect and takeover, admits the Steward config as the [session](#the-steward-session) does: it refuses a managed Steward copy (a `managed.toml` beside `config.toml`) and an inline `[managed]` table, and needs `mode = "steward"`, a `[steward] name` and a private config location that is a Git work tree.

For each Director copy on each named machine, the dry run downloads `config.toml` and `managed.toml`, renders new managed settings and checks the combined config.
It writes `plan.json` and, for each copy that would change, `files/<machine>/<director>/previous.toml` and `managed.toml` previews into the new plan directory, and prints:

- each copy's status: `change`, `unchanged` or `refused` with the reason;
- each managed key added, changed or removed, and whether it needs approval;
- a line diff of the published managed file;
- the planned `scp` commands, with preview source paths replaced by private temporary inputs during [apply](#apply), and the `ssh` command that runs `config reload` on the copy, all part of what the plan digest covers;
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
For each copy that changes, it then runs the four planned commands:

1. copy the previous managed file to `managed.toml.rev-<first 16 hex digits of its sha256>` next to it, keeping the old revision;
2. copy the new managed file over `managed.toml`;
3. read `managed.toml` back and compare its sha256 with the plan;
4. once it matches, run `guildhall config reload --copy <copy_dir>` on the copy over SSH, which records the new digest as authorized in the copy's own store ([how the Steward acts](director-modes.md#how-the-steward-acts)).

Every apply that writes a copy runs the reload, whether or not the plan needed approval, so a routine rollout is never an unauthorized revision on the Director.
A failure on one copy does not stop the others.
The receipt `receipt-<UTC time>.json` in the plan directory records each copy's status (`applied`, `unchanged`, `failed`, `verify_failed` or `reload_failed`), its before and after digests, its read-back digest when verification reaches digest comparison, the revision file and every attempted command with its error, and, for the reload, what it printed.
Read-back follows successful uploads only; failed transfers are recorded as failed and may leave partial remote files without a read-back digest.
A `reload_failed` copy has the new managed file, published and verified, but its reload failed, for example because the machine's `guildhall` predates `config reload` and says it does not know the command; the copy is not undone, and its Director refuses the new revision until a reload authorizes it, which the Guildmaster can run by hand on that machine.
Apply exits 1 unless every copy is applied or unchanged; a plan that is no longer current needs a new dry run.
Applying an unchanged plan performs no remote writes and runs no remote command, but still writes a local receipt.

### Publication and writer containment

The [managed-file checksum framing](configuration.md#config-file) makes combined loading explicitly refuse incomplete or corrupted transfers, including syntactically valid truncated TOML.
This is integrity detection, not atomic publication: non-atomic scp may transiently refuse reads and does not prove uninterrupted config availability.
The `.rollout.lock` file in the private config directory excludes concurrent applies, [collections](#collection), takeovers and [secrets](secrets.md#the-steward-pushes-them) sets and applies using that directory on this Steward.
It does not protect against another host, another private directory or a manual managed-file writer; designate one Steward writer for each fleet and do not edit managed files during rollout.
The lock holds its owner's record: the PID and OS start token of the process that took it, its command line and when it took it. A refusal names that owner, or an unknown owner since the file's modification time when the lock has no readable record, as an older binary's has.
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
Roster values that reach the command line are restricted to characters that no remote shell or `scp` interprets.
Each `scp` run is bounded by a two-minute timeout; a run stopped by its timeout is reported as a timeout, and one that fails before transferring leaves its destination unchanged.

### Remote commands

Besides the copies, the Steward runs only fixed Guildhall commands on a machine, never a shell of its own: `ssh -o BatchMode=yes -- <ssh_alias> <guildhall> <command> --copy <copy_dir>`, run from `PATH` as an argument list, where `<guildhall>` is the machine's `guildhall_path` or `guildhall`.
The allow-list is exactly ([decision](decisions/steward-fixed-remote-commands.md), [collection commands](decisions/steward-collection-commands.md)):

- `config reload --copy <copy_dir>`, which [apply](#apply) runs on each copy it wrote;
- `director mode --json --copy <copy_dir>`, which only reads the copy's mode and managed digest;
- `lesson list --json --pending --copy <copy_dir>` and `incident list --json --pending --copy <copy_dir>`, which only read the copy's items not yet [collected](#collection);
- `director collected --copy <copy_dir>`, which marks the items of the receipt collection copied to the copy as collected;
- `messages receive --copy <copy_dir>`, which receives the [Steward message](#steward-messages) copied to the copy's drop file ([decision](decisions/steward-messages-directors.md));
- `director status --json --copy <copy_dir>`, which only reads the copy's status feed, for the [Director health](#director-health) checks ([decision](decisions/steward-reads-director-status.md)) and an approved [takeover](#takeover);
- `secrets check --json --copy <copy_dir>`, which only reads: whether the machine's [secrets file](secrets.md#secrets-check) decrypts with its key and which names it holds, never a value, run by [`steward secrets`](#secrets) ([decision](decisions/steward-secrets-push.md));
- `director reload --copy <copy_dir>`, reserved for [release upgrades](releases.md#guildhall-director-reload) and refused until that is built;
- for an approved [takeover](#takeover) only ([decision](decisions/steward-setup-plans.md)): `director pause --copy <copy_dir>` and `director resume --copy <copy_dir>`; `backup now --copy <copy_dir>` and `store export --out <dir> --copy <copy_dir>`; and `director enrol --steward <label> --copy <copy_dir>`.

A value other than the copy directory is held to the same rule: `--out` takes an absolute path in the restricted character set and `--steward` a lowercase id.
Any other command, flag or argument is refused before `ssh` runs, and so is an alias that is not a plain roster alias.
`ssh` joins the arguments with spaces for the remote side, which then parses them again, through `cmd.exe` or PowerShell on a Windows OpenSSH server and a POSIX shell elsewhere; every value is restricted to letters, digits, `.`, `_`, `-`, `/` and a drive's `:`, which all of them read the same way, so nothing is quoted.
Each `ssh` run is bounded by a two-minute timeout and is cancelled as `scp` is, killing its process group on Unix and its process elsewhere.
A command that exits non-zero, a refusing machine and a timeout are each reported with their diagnostic.

## Collection

```sh
guildhall steward collect [--machine <label>...] [--copy <dir>]
```

Directors file [lessons](director-modes.md#lessons) and [maintenance incidents](messaging.md#maintenance-tickets-to-the-steward) in their own copies, standalone or managed.
`steward collect` brings those of the enrolled Director copies, on the named machines or every machine, into the private config checkout, as deterministic code that spends no model tokens.
It reaches a copy only with the fixed [remote commands](#remote-commands) and `scp`, and a standalone copy, which is not in the roster, keeps its queue locally.
One round:

1. reads each copy's pending items with `lesson list --json --pending` and `incident list --json --pending` over SSH, each list capped at 1 MiB, the rest collected in later rounds;
2. takes `.rollout.lock` in the private config, and stops with `busy`, changing nothing, when a rollout or another collection holds it;
3. writes each item not yet committed to `lessons/<machine>/<origin>-<id>.md` or `incidents/<machine>/<origin>-<id>.json`, with its content digest, and commits only those files, one commit per machine, through a private index, so the Guildmaster's uncommitted `roster.toml` and `managed.toml` edits are never swept in;
4. writes each [triage](#triage) answer recorded since into its item's file, in one more commit;
5. pushes what the remote lacks, fast-forward only and bounded by a timeout; when the remote has moved ahead, it fetches it, replays its commits onto the new tip once and pushes again, and otherwise reports the failure;
6. copies each copy a receipt, `state/steward/collect-receipt.json`, listing each item's origin, id, digest and the pushed commit, and runs `director collected` there, which marks the items whose origin and digest match as collected, reports any other, and deletes the receipt;
7. opens one triage decision for each collected file that has none.

An item already committed with the same digest is not written again; only its mark is sent again, which covers a crash between the commit and the mark and makes a second run change nothing.
An unreachable machine, or a copy whose mark fails, is reported and retried in the next round without failing the others, and its items stay pending on the Director until a mark succeeds.
A collected lesson file is a front matter of `key: "<JSON string>"` lines (`schema`, `machine`, `director`, `origin`, `id`, `digest`, `title`, `filed_at`, `collected_at` and `triage`) before the lesson's body as filed; a collected incident is one JSON document with the same fields, flat, and the incident with its observations.
The command prints the round as JSON, `{commit, copies, answers, opened}`, each copy with its items, their `new`, `marked` and any `problem`, and its `error`; it exits 1 when the lock is busy, the round fails, or a copy or item was not collected.

### Triage

Collection opens one decision per collected item on two reserved tasks of the Steward store: `steward/incidents`, with the options `accept`, `defer` and `decline`, and `steward/lessons`, with `adopt` and `decline`.
Each is an approval bound to the item's content digest, with the id `<kind>-<first 16 hex digits of the sha256 of the item's path>`, so an item has one decision however many rounds see it.
Only the Guildmaster answers it, outside any session, with `guildhall decide answer --copy <dir> --repo steward --task incidents|lessons --decision <id> --answer-file -`; `decide answer` refuses it from inside one.
The next round writes the answer into the item's file as `triage`, `triage_decision`, `triage_outcome` and `triage_note`.
Triage authorises no work: an accepted incident is recorded as the Guildmaster's to create a task, and the Steward does nothing to a Director for it; an adopted lesson is drafted by the Steward as an ordinary private-config change, which a later approved rollout ships (R1.32).
The Director's own record stays `collected`.
The `guildhall-steward-triage` skill presents each decision.

### Scheduled collection

With `[steward] collect_every` set, the Steward session runs a round at its start and then every `collect_every`, as the supervisor's deterministic code off its decision path, the way a Director runs its daily prune.
Rounds run one at a time, a round that finds the lock busy is skipped, and failures are reported on the supervisor's standard error.
The third busy round in a row sends the Steward's inbox one `lock-busy` message, with the [`director` sender tag](#inbox), naming the lock's owner and since when it has held the lock, which wakes the session; any round that is not busy starts the count again.
Its id is a digest of the owner record, so a reload, which also starts the count again, sends no second message for the same owner.
It does not say the owner crashed, since a takeover apply can hold the lock for 30 minutes, and nothing removes the lock: recovery stays [manual](#publication-and-writer-containment).
A round that opens triage decisions sends the Steward's inbox one `triage-opened` message per kind, with the [`director` sender tag](#inbox), which wakes the session; a round with nothing new sends nothing and wakes nothing.
Unset, the default, nothing is collected on a schedule, and nothing polls or spends model tokens because an item is queued (R1.7).
Each scheduled run then sends its [Steward messages](#steward-messages), even when the round found the lock busy.

## Secrets

```sh
guildhall steward secrets set --config <steward.toml> <machine> <NAME>
guildhall steward secrets plan --config <steward.toml> --machine <label> [--machine <label>...] --plan <new dir>
guildhall steward secrets apply --config <steward.toml> --machine <label> [--machine <label>...] --plan <dir> --approve <plan digest>
```

`steward secrets` keeps each enrolled machine's [secrets](secrets.md) as sops ciphertext in the private config, `secrets/<machine>.sops.yaml`, encrypted to the machine's `age_recipient` and the roster's `recovery_recipient`, with `.sops.yaml` generated from the roster.
`set` reads a value from the terminal or standard input, never the command line, encrypts it at once and commits and pushes only ciphertext.
`plan` and `apply` push a machine's committed ciphertext with `scp`, read it back by digest and run the read-only [remote command](#remote-commands) `secrets check` there, after the Guildmaster approves the plan's exact digest on the reserved task `steward/secrets`; plans and receipts hold names and digests, never a value.
It is a separate command from [rollouts](#apply), which stay credential-free; [secrets](secrets.md#the-steward-pushes-them) describes it in full.

## Steward messages

The Steward tells an enrolled Director about issues it finds with a **Steward message**, a `steward-report` in the Director's external inbox ([messaging](messaging.md#steward-messages), [decision](decisions/steward-messages-directors.md)).
A Steward message informs only: it is not dispatch, a task, an approval or a push of configuration, and the Director acts on nothing it says without the Guildmaster (R1.6, R1.7, R1.8, R1.15).
The supervisor's scheduled run sends them as deterministic code, never the Steward's model, so they need `collect_every`; each run, after its collection round:

1. finds the reports: `rollout-failed` for each enrolled copy whose newest rollout receipt under `rollouts/` is `failed`, `verify_failed` or `reload_failed`, `unreachable-recovered` for each copy the round reached after it had been unreachable, by the round's own reads, for at least `[steward] report_unreachable_after` (one hour by default, at least a minute), and the [Director health](#director-health) reports `director-stuck`, `director-unhealthy` and `release-behind`;
2. records each report in the Steward store's outbox, `pending`, under its id `<kind>.<first 16 hex digits of the sha256 of its copy and issue>`, so one issue on one copy is one message however often a run finds it;
3. delivers every pending entry, one at a time: it copies the envelope with `scp` to `state/steward/message.json` in the copy, then runs the fixed [remote command](#remote-commands) `messages receive --copy <copy_dir>`, and records the copy's answer, `delivered` with its receipt or `refused` with its reason.

A refusal, such as an inbox the Director closed to the Steward or a full mailbox, is final and never retried.
A delivery that fails, such as an unreachable or sleeping machine or a `guildhall` too old to know the command, stays `pending` with its error, and the next run retries it, so nothing is lost while a machine sleeps.
`guildhall messages outbox [--all] --copy <steward copy>` lists the entries not delivered, or every one.
`state/steward/` is made by the copy's `director enrol`, `config reload` and pending lists, since `scp` makes no directory.
A copy that is not in the roster, standalone or removed, gets no Steward message.

### Director health

Each scheduled run reads, after its collection round, every enrolled copy's status with the fixed read-only command `director status --json --copy <copy_dir>`, skipping a copy the round found unreachable ([decision](decisions/steward-reads-director-status.md)).
The feed's `health` block gives the newest session's supervisor (`alive`, `gone`, `unverified`, or `none` with no session or a stopped one), the last turn signal its runtime's hooks recorded, the messages waiting for the Director, local and external, with the oldest one's arrival, and the release the session runs beside the copy's pin ([status](director.md#status)).
With the span `[steward] report_director_after` (one hour by default, at least a minute), the run finds three conditions:

- `director-stuck`: messages wait, and the Director has taken no turn for the span since the later of its last turn and the oldest waiting message's arrival; a stopped session, or none, with a message waiting that long is stuck too;
- `director-unhealthy`: the session's supervisor is gone while the session is not stopped, reported at once, or the session has stayed `recovering` for the span since it started;
- `release-behind`: the session runs a release older than the copy's pin, by semantic version order, and the Steward has found it so for the span.

The Steward store keeps each condition per copy with the time it first found it and a mark naming what holds, such as the release and pin, and the report's id is made from both, so one condition is one message, however many runs find it, until a run sees it clear; a condition that clears and returns, or holds with a new mark, is a new message.
A copy whose status cannot be read keeps its conditions as they were, and the failure goes to the supervisor's standard error, as does a copy whose `guildhall` predates the `health` block; a copy on a `guildhall` that knows Steward messages but not these kinds refuses them, which the outbox records as a final refusal.
The `director-stuck` check compares the copy's times with the Steward's clock, so it relies on the machines' clocks agreeing; a runtime with no turn hooks records no last turn, so its Director counts as stuck once a message has waited the span unacknowledged.

## Takeover

```sh
guildhall steward takeover --config <steward.toml> --machine <label> --director <name> --copy <dir> [--ssh-alias <alias>] --plan <dir> --dry-run
guildhall steward approval request --plan <dir> [--copy <steward copy>]
guildhall steward takeover --config <steward.toml> --plan <dir> --apply --approve <plan digest>
guildhall steward takeover --config <steward.toml> --plan <dir> --abandon
```

A takeover adopts an existing standalone Director copy into the roster as managed by this Steward, keeping its store, tasks, decisions and local settings ([decision](decisions/steward-setup-plans.md)).
It is built for Linux machines; native Windows comes later.
Each of its modes admits the Steward config as [rollout](#dry-run) and collect do, with the session's refusals, and needs the private config location to be a Git work tree.

**Dry run.** The dry run reads the copy with `scp` and the read-only [remote commands](#remote-commands): its `config.toml`, its mode, which must be `standalone` with no `managed.toml`, and its session.
It refuses, changing nothing, a copy that is not a director copy named `--director`, has inline `[managed]` settings or a repository whose `owner_machine` is not `--machine`, and a Director or copy directory the roster already lists.
`--machine` is a roster label; a machine not yet in the roster needs `--ssh-alias`, and is proposed as a new `linux` machine with the `director` role.
It writes `plan.json`, a `guildhall.setup-plan/1` document, and `files/managed.toml` into the plan directory, and prints the copy, the roster diff and each step with its command.
The first `managed.toml` has no managed key, so the copy's effective settings do not change; later rollouts bring the Steward's settings.
The digest binds the copy's `config.toml`, its mode, the roster at the private config's `HEAD`, the planned file and every step, but not the session's state, which moves on its own; a dry run again into the same plan directory gives the same digest when nothing changed, and one whose apply has started is refused.

**Approval.** Every takeover needs the Guildmaster's approval of its exact digest.
`steward approval request --plan <dir>` opens the decision `setup-<first 12 hex digits>` on the reserved task `steward/setup`, which only the Guildmaster answers, outside any session; inside one, `--approve` stands only on their answer `approve` ([approval in a Steward session](#approval-in-a-steward-session)).
`--apply` without `--approve` and the plan's digest refuses and runs no step.

**Apply.** Apply holds `.rollout.lock` throughout and refuses with `busy` while it is held.
The first apply plans again and refuses, running no step, unless the digest is the same, so a copy, roster or planned file that changed since the dry run stops it.
It then runs the steps in order, each checking before it acts and verifying after:

1. `pause`: a `running` session is paused with `director pause`, so it starts no new work; a session already paused, stopped or absent is left as it is, and a `recovering` or `stopping` one stops the apply until it settles.
2. `drain`: `director status --json` must list no running attempt; while one runs, the apply ends `waiting drain` and is run again later.
3. `backup`: `backup now` for a copy with a `[backup]` table, otherwise `store export --out <copy>/state/steward/takeover/<plan directory name>`.
4. `managed`: the plan's `managed.toml` is copied beside `config.toml` with `scp` and read back.
5. `enrol`: `director enrol --steward <[steward] name>`.
6. `reload`: `config reload`, after which `director mode --json` must report the copy `managed`, by this Steward, with the published digest authorized.
7. `resume`: `director resume`, only for a session the takeover paused.
8. `roster`: the entry is added to `roster.toml` at the private config's `HEAD`, as text so comments and layout are kept, committed alone through a private index and pushed, as [collection](#collection) commits.

The step log `steps.json` in the plan directory records each step's state, `started`, `done`, `waiting` or `failed`, with what it printed and found.
It is written before a step acts and after it ends, so an apply killed at any point and run again resumes at the first step not done, and never acts again for a step done; a later apply checks only that the copy's `config.toml` is unchanged.
`managed`, `enrol` and `reload` run back to back while the session is paused; a stop between them is repaired by applying again.
Apply prints each step's record and ends with `done`, `waiting <step>: <why>` or `failed <step>: <diagnosis>`, and exits 0 only when done.
Applying a finished plan checks that the copy is still managed and the roster still holds the entry, and changes nothing.

**Abandon.** `--abandon` gives up a plan before its copy is changed: it resumes a session the takeover paused and marks the plan abandoned, so it never applies.
Once the `managed` step has run it refuses, since the copy may hold the plan's `managed.toml`; such a plan is finished with `--apply`.

**Failures.** A takeover never deletes or overwrites a store, a copy or a file it did not create.
When a step fails, the Steward reads the step log and the command's diagnostic, tells the Guildmaster what is wrong, and may apply the same plan again once it is fixed; any other fix is a new dry run, approved again.

## Managing a Director copy

A Director copy is standalone until it is enrolled; a rollout reaches only its `managed.toml` and never enrols it ([standalone and managed Directors](director-modes.md)).
A [takeover](#takeover) brings a running copy under this Steward in one approved plan.
To do it by hand instead:

1. Add the machine and the copy to `roster.toml`, with `guildhall_path` if `guildhall` is not on the account's `PATH`.
2. On the Director's machine, copy [`examples/managed.toml`](../examples/managed.toml) beside the copy's `config.toml`, as its first `managed.toml`.
3. There, run `guildhall director enrol --steward <label>` on the Guildmaster's word.
   Enrolment never authorizes that first digest, so the copy refuses every command that needs its settings until it is authorized.
4. Authorize it: run a rollout to the machine, whose apply runs `config reload` on each copy it writes, or run `guildhall config reload --copy <dir>` there by hand.
   A rollout that leaves the copy unchanged writes nothing and runs no reload, so the hand reload is the way when the Steward's settings already match.
5. Check with `guildhall director mode` on the copy, or `guildhall steward status` here: it shows `managed`, the Steward's label and the digest, authorized.

A `managed.toml` changed on the machine by anything but a rollout and its reload, such as a hand edit, is not applied: the copy records it `observed-untrusted` and raises an approve/reject decision for the Guildmaster in its own `needs_me` ([observed-untrusted alerts](director-modes.md#observed-untrusted-alerts)), and `steward status` shows the digest as untrusted.

`guildhall director leave`, run on the Director's machine on the Guildmaster's word, returns the copy to standalone; then remove it from the roster.

## Revisions and active tasks

A managed revision is the sha256 of its `managed.toml`, which the plan and receipt record; plans separately bind the fetched local config digest.
For task configuration binding, see [tasks](tasks.md#the-bound-workflow); the managed digest alone is not the entire effective config.
A rollout keeps each previous revision on the machine and never deletes one.
A revision naming a `release` moves the copy's pin at its reload, and active tasks keep the release they recorded ([release upgrades](releases.md#tasks-keep-their-release)).

## Not implemented yet

- The rest of R1.31, the release pin and side-by-side release installs designed in [release upgrades](releases.md): the Guildhall home, `guildhall release install` and `status`, the pin, `guildhall release use` and `adopt`, the PATH launcher, tasks recording and running on their release, a standalone copy's `guildhall release rollback` and `release prune`, and the managed pin, `release` and `release_sha256` in `managed.toml` applied by `config reload`, are [built](releases.md#built-so-far). A rollout of a new `release` changes the pin once the release is installed or its binary is in the machine's `releases/.incoming/<version>/`; the rollout copying that binary itself and the remote `director reload` are not built yet.
- `rollback`, which will restore a preserved revision for new tasks.
- Tool installs (`guildhall steward install`, R1.7, R1.50) and audits (R1.32); learnings are [collected and triaged](#collection), and an adopted lesson ships by an ordinary rollout.
- Provisioning a new Director copy, the setup engine and doctor, bootstrapping a bare machine, and takeover on native Windows: the later slices of guildhall/d128, under the same [setup plans](decisions/steward-setup-plans.md) (R1.43-R1.51).
- The [takeover](#takeover)'s live acceptance, which the Guildmaster runs once on the laptop's WSL Director copy: CI runs it only on a stand-in fleet and a local copy.
- Platform support: tests replace `scp` and `ssh` with stubs on Linux, Windows and macOS, and no platform is claimed as supported for rollouts until acceptance passes there (R1.27).
- The Steward session's live acceptance: CI runs it against a stand-in runtime only, so it is not claimed until the Guildmaster has run it once with three Director hosts (R1.27, R1.30).
- Collection's live acceptance, d96's, which the Guildmaster runs once after `guildhall/d124` merges: collection is claimed on Windows only once it passes there (R1.27, R1.30).
