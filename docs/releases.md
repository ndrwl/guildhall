# Release upgrades

Everything on this page is designed, not built.
No command, config key, file or store record it names exists yet; later slices build each contract red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [release upgrades](decisions/0051-release-upgrades.md).
It implements R1.31: a release pin, side-by-side installs, new tasks only, and rollback.

The same rules as [standalone and managed Directors](director-modes.md) apply:

- no feature requires a Steward: a standalone Director installs, pins, rolls back and prunes releases on its own;
- the Steward acts by command over SSH and never messages a Director ([Director messaging](messaging.md#who-may-send));
- a managed pin the receiving store has not authorized is `observed-untrusted` and is not applied.

## What already holds

On `main` today:

- A release is one binary per platform, named `guildhall_<os>_<arch>` and listed in `SHA256SUMS` (D5), with its skills embedded (D30).
- Rollouts copy `managed.toml` with `scp` and keep every previous revision (D12, D14).
- `guildhall task new` binds the configuration digest to the task, so a new managed revision reaches new tasks only (R1.62).

Nothing records which release a task runs, and a machine has exactly one `guildhall` on `PATH`.

## Layout

Each machine keeps its releases side by side under the Guildhall home:

```text
<guildhall home>/
  bin/guildhall              the PATH launcher
  releases/
    v1.4.0/guildhall         installed binary (guildhall.exe on Windows)
    v1.4.0/SHA256            the verified sha256 of that binary
    v1.3.2/...
    .incoming/               copies not yet verified; never run
  copies                     inventory of copy directories using these releases

<copy directory>/
  config.toml
  managed.toml               managed copies only
  release.pin                this copy's pinned version, one line
```

The launcher and installations are shared; pins and stores belong to individual copies.
Every copy must register its absolute directory in the home's inventory before using shared releases, including standalone copies; pruning uses this inventory, not the Steward roster.

A release directory is written once and never changed.
`release install` copies the binary into `.incoming/`, checks its sha256 against the expected value, then renames the directory into `releases/<version>/` in one step.
A directory whose binary no longer matches its `SHA256` file is reported as damaged and never run.

## The pin

The pin names the release that new work uses in one Director copy.
Commands select the copy with `--copy <dir>`, or use the current directory only when it is itself a copy directory; absent or ambiguous selection is refused before execution.
Task commands carrying `--store` select the copy owning that store; an explicit copy must agree.
This selection applies to release commands, configuration reload, task creation and Director lifecycle commands.

- **Standalone**: `guildhall release use <version>` sets it.
- **Managed**: `managed.toml` carries `release` and `release_sha256`; the Steward changes them by [rollout](steward.md), whose preview shows the old and new version and whose plan digest the Guildmaster approves.
  `guildhall config reload` authorizes the revision as [director modes](director-modes.md#how-the-steward-acts) describes and only then writes that named copy's `release.pin`; other copies' pins and stores are untouched.
  If the pin is absent or its executable is missing or damaged, explicit `config reload --copy <dir>` uses the recovery dispatch below; this changes how reload starts, not how it authorizes the revision.
  A managed release pin is a managed key, so `release use` refuses in managed mode and names the rollout.

Each pin change, by either route, is a `release-pin` event in that copy's store with the new and previous version, the source (`use`, `reload`, `rollback` or `guildmaster-approved`) and the time.
The copy's `release.pin` is a cache of its newest event, written after the event and atomically by rename.
If that copy's file and newest event disagree, the shared configuration-admission boundary refuses commands that need effective settings and raises a system alert, as for an observed-untrusted managed revision; `guildhall release use` or a reload of the authorized revision repairs it.

## The PATH launcher

`bin/guildhall` is the only `guildhall` on `PATH`.
It is a small fixed program installed with the first release and replaced only by an explicit `guildhall release install --launcher`.

1. It resolves the selected copy before choosing an executable.
   For new work and Director commands it reads that copy's `release.pin`; for an existing task it reads the immutable version and sha256 from the task's owning store instead, using the store, repository and task arguments.
   It loads no effective configuration.
   Missing or conflicting task identity is refused, never treated as new work.
2. If `releases/<version>/guildhall` exists and matches its `SHA256` (and, for a task, the recorded sha256), it runs it with the same arguments and standard streams and returns its exit code (on Unix by `exec`).
3. If the pinned release is missing or damaged, it never falls back to another release: it exits non-zero and prints the version, the path it checked and the fix (`guildhall release install <version>` or, managed, a rollout).
   Recovery dispatch is limited to `guildhall release ...` and explicit `guildhall config reload --copy <dir>`: it runs the command on the newest installed, verified release and says which.
   If none exists, it refuses; it never runs an unverified `.incoming/` binary.
4. If the selected copy's `release.pin` is absent, the same recovery dispatch applies.
   Other non-task commands refuse, naming `guildhall release use` for standalone setup or `guildhall config reload --copy <dir>` for managed setup.
   Existing tasks do not depend on the current pin.

Recovery `config reload` must verify that the selected copy is managed and perform the same combined-config validation, migration and mode checks, and receiving-store revision authorization as ordinary reload.
An absent pin or unusable executable does not bypass those checks or require admitting current effective settings before reload can repair them.
Unsupported config or store schemas and failed authorization refuse the operation without changing the pin.
Only after authorization and verification of the revision's target binary may reload record `release-pin` and write its cache.
Dispatch itself neither authorizes a revision nor changes a pin, and grants no fallback to task or Director execution.

The launcher replaces any inherited `GUILDHALL_RELEASE` with `GUILDHALL_RELEASE=<selected version>` for the child, including when a newer Director launches an older task; the inherited variable is never a selection input.
Each release refuses to run if that variable names a different version from its own, so the launcher cannot be confused with a mislabelled directory.
Each dispatched executable validates the selected copy's settings or task binding before acting.
Copy selection is preserved in child invocations and Director restarts.

## Tasks keep their release

`guildhall task new` records the selected copy's pinned version and its sha256 on the task, beside the configuration digest.
Every later step of that task, including initial Artisan launches, subsequent steps and resume commands (`launch.ResumeArgv`), enters the task-aware launcher with the owning store, repository and task identity.
The launcher executes `releases/<recorded version>/guildhall`, not the current pin, and replaces the child release environment as above.
Herdr's registered resume argv continues to start with the plain command `guildhall`, followed by `artisan resume` and the existing store, state, repository, task, step and attempt arguments; no executable path is registered and Herdr needs no change.
A task whose recorded release is missing or damaged refuses to advance and raises an alert naming the version; it is never moved to another release.

## How a running Director picks up a new release

The Steward never tells the Director by message; it acts by command, in order:

1. `scp` the new binary into `.incoming/<version>/` and the new `managed.toml` (D12);
2. `guildhall config reload --copy <dir>` over SSH, using recovery dispatch if the pin is absent or its executable is missing or damaged, which authorizes the revision, verifies an already installed target or runs `release install` from `.incoming/` against `release_sha256`, records `release-pin` in that copy's store and then writes that copy's pin;
3. `guildhall director reload --copy <dir>` over SSH.

A standalone Guildmaster runs the same three steps by hand: `guildhall release install <version> --from <file> --sha256 <hex>`, `guildhall release use <version>`, `guildhall director reload`.
Steps 1 and 2 alone already make new tasks use the new release, because `task new` reads the pin; step 3 moves the Director's own session onto it.

### `guildhall director reload`

`director reload` asks the running Director to restart its agent session on the current pin and settings.
Its request, its `--now` form and the end-of-turn rule a standalone Director applies it by are built, as [the Director](director.md#reload) describes; moving onto a new release's binary lands with release pins.
It records a `director-reload` request event and returns at once; it never interrupts a step.

- The Director checks for a pending reload at each step boundary, the same boundary where it applies a new managed revision.
  At the first one, it records `director-reload` `applied` with the old and new release and config digest, stops its agent session cleanly and starts it again through the launcher with the same explicit copy selection in the same Herdr tab.
- What reloads: the Director's own binary (from the pin), its effective settings (the newest authorized revision) and its rendered prompts and skills.
- What does not: active tasks, their bound configuration and recorded release, the mailbox, and Artisan sessions, which keep running on their recorded release.
- A Director with no step in flight applies it within one poll; one that is not running records the request as `applied` on its next `guildhall director start`.
- Repeated requests before the boundary collapse into one, by the mutation-id rule every store command follows.

`director reload` is not one of the role lifecycle verbs (`guildhall director start|stop|status|resume`); it is the one fixed remote command besides `config reload` that the Steward may run.

## Rollback

Rollback moves the pin back; it never touches an installed release or an active task.

- Standalone: `guildhall release rollback` sets the pin to the previous version from the `release-pin` events, with source `rollback`.
- Managed: the Steward rolls back by rollout of the previous `managed.toml` revision, which carries the previous `release`, followed by `config reload --copy <dir>` and `director reload --copy <dir>`.
  If the current pin is absent or its executable is missing or damaged, reload uses recovery dispatch, still authorizes the rolled-back revision and verifies its installed target before changing the pin; Director reload runs only after that succeeds.

The previous release is protected from pruning, so rollback never needs a download.

## Pruning

`guildhall release prune` runs automatically after every pin change and can be run by hand.
It keeps the newest three installed releases by install time and removes others only if none of these holds across every copy in the shared home's inventory:

- it is pinned by any copy;
- it is any copy's rollback target, the previous version in that copy's `release-pin` events;
- a running or resumable task in any copy records it, which includes every task not yet `done` or `cancelled`.

Pruning refuses deletion if any inventoried copy's pin or store cannot be read or disagrees.
Reference checks and deletion must be serialized with pin changes, task creation and copy registration so a new reference cannot race deletion.
So more than three releases may stay when copies or tasks still need them; retention is fixed, not configurable.
Each removal is a `release-pruned` store event with the version, its sha256 and the reason it was eligible; a release that was kept past the limit is listed with the reason in `guildhall release status`.
Prune removes only `releases/<version>/` directories and stale `.incoming/` copies, never the launcher, the store or `managed.toml` revisions.

## Commands and store records

| Command | Does |
|---|---|
| `guildhall release install <version> --from <file> --sha256 <hex>` | Verifies and installs a release side by side. |
| `guildhall release install --launcher` | Replaces the PATH launcher from the running release. |
| `guildhall release use <version>` | Standalone: sets the pin, records `release-pin`, then prunes. |
| `guildhall release rollback` | Standalone: moves the pin to the previous version, then prunes. |
| `guildhall release prune` | Keeps the newest three plus releases protected by any copy. |
| `guildhall release status` | Prints the selected copy's pin, shared installed releases, which copies and tasks hold each, and any damage or mismatch. |
| `guildhall director reload` | Requests an agent restart at the next step boundary. |

| Store record | Contract |
|---|---|
| Release pin event | New and previous version and sha256, source `use`, `reload`, `rollback` or `guildmaster-approved`, and the time; never changed. |
| Task release | Version and sha256 recorded at `task new`; never changed. |
| Director reload event | `requested` with its mutation id, then `applied` with the old and new release and config digest. |
| Release pruned event | Version, sha256, the time and why it was eligible. |

## Testing

Contract tests use temporary homes and test-built binaries with stamped versions; CI makes no model calls and uses no SSH.
They cover: the launcher on a present, missing, damaged and absent copy pin and the `release` exception; refusal of missing or conflicting copy/task selection; a rollout to copy A leaving copy B unchanged; a pin change leaving an active task on its release; plain-command Herdr resumes and initial Artisan launches from a newer Director with its inherited release environment replaced; reload applied only at a step boundary; per-copy rollback; and fixed three-release retention with every cross-copy prune protection, unreadable copies and concurrent reference creation.
Each runs on a standalone and a managed copy through the shared helper in [director modes](director-modes.md#testing-both-ways).
Managed recovery cases cover initial pin creation, rollback from a missing or damaged executable to a verified installed release, and refusal without pin changes for failed authorization, unsupported schemas or no verified recovery executable; task and Director commands must still refuse rather than fall back.
