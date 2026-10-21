# Release upgrades

Everything on this page is designed, not built.
No command, config key, file or store record it names exists yet; later slices build each contract red-first, as [contracts](contracts.md#method) describes.
The decision is recorded in [release upgrades](decisions/release-upgrades.md).
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
  release.pin                the pinned version, one line
```

A release directory is written once and never changed.
`release install` copies the binary into `.incoming/`, checks its sha256 against the expected value, then renames the directory into `releases/<version>/` in one step.
A directory whose binary no longer matches its `SHA256` file is reported as damaged and never run.

## The pin

The pin names the release that new work uses on this machine.

- **Standalone**: `guildhall release use <version>` sets it.
- **Managed**: `managed.toml` carries `release` and `release_sha256`; the Steward changes them by [rollout](steward.md), whose preview shows the old and new version and whose plan digest the Guildmaster approves.
  `guildhall config reload` authorizes the revision as [director modes](director-modes.md#how-the-steward-acts) describes and only then writes `release.pin`.
  A managed release pin is a managed key, so `release use` refuses in managed mode and names the rollout.

Each pin change, by either route, is a `release-pin` store event with the new and previous version, the source (`use`, `reload`, `rollback` or `guildmaster-approved`) and the time.
`release.pin` is a cache of the newest event, written after the event and atomically by rename.
If the file and the newest event disagree, the shared configuration-admission boundary refuses commands that need effective settings and raises a system alert, as for an observed-untrusted managed revision; `guildhall release use` or a reload of the authorized revision repairs it.

## The PATH launcher

`bin/guildhall` is the only `guildhall` on `PATH`.
It is a small fixed program installed with the first release and replaced only by an explicit `guildhall release install --launcher`.

1. It reads `release.pin` and nothing else; it opens no store and loads no config.
2. If `releases/<version>/guildhall` exists and matches its `SHA256`, it runs it with the same arguments, environment and standard streams and returns its exit code (on Unix by `exec`).
3. If the pinned release is missing or damaged, it never falls back to another release: it exits non-zero and prints the version, the path it checked and the fix (`guildhall release install <version>` or, managed, a rollout).
   The one exception is `guildhall release ...`, which it runs on the newest installed, verified release and says which, so a machine can always be repaired or rolled back.
4. If `release.pin` is absent, it behaves as for a missing release, naming `guildhall release use`.

The launcher sets `GUILDHALL_RELEASE=<version>` for the child, and each release refuses to run if that variable names a different version from its own, so the launcher cannot be confused with a mislabelled directory.

## Tasks keep their release

`guildhall task new` records the pinned version and its sha256 on the task, beside the configuration digest.
Every later step of that task, including Artisan launches and resume commands (`launch.ResumeArgv`), runs `releases/<recorded version>/guildhall` directly rather than the launcher, so a pin change never moves an active task.
A task whose recorded release is missing or damaged refuses to advance and raises an alert naming the version; it is never moved to another release.

## How a running Director picks up a new release

The Steward never tells the Director by message; it acts by command, in order:

1. `scp` the new binary into `.incoming/<version>/` and the new `managed.toml` (D12);
2. `guildhall config reload --copy <dir>` over SSH, which authorizes the revision, runs `release install` from `.incoming/` against `release_sha256`, writes the pin and records `release-pin`;
3. `guildhall director reload --copy <dir>` over SSH.

A standalone Guildmaster runs the same three steps by hand: `guildhall release install <version> --from <file> --sha256 <hex>`, `guildhall release use <version>`, `guildhall director reload`.
Steps 1 and 2 alone already make new tasks use the new release, because `task new` reads the pin; step 3 moves the Director's own session onto it.

### `guildhall director reload`

`director reload` asks the running Director to restart its agent session on the current pin and settings.
It records a `director-reload` request event and returns at once; it never interrupts a step.

- The Director checks for a pending reload at each step boundary, the same boundary where it applies a new managed revision.
  At the first one, it records `director-reload` `applied` with the old and new release and config digest, stops its agent session cleanly and starts it again through the launcher in the same Herdr tab.
- What reloads: the Director's own binary (from the pin), its effective settings (the newest authorized revision) and its rendered prompts and skills.
- What does not: active tasks, their bound configuration and recorded release, the mailbox, and Artisan sessions, which keep running on their recorded release.
- A Director with no step in flight applies it within one poll; one that is not running records the request as `applied` on its next `guildhall director start`.
- Repeated requests before the boundary collapse into one, by the mutation-id rule every store command follows.

`director reload` is not one of the role lifecycle verbs (`guildhall director start|stop|status|resume`); it is the one fixed remote command besides `config reload` that the Steward may run.

## Rollback

Rollback moves the pin back; it never touches an installed release or an active task.

- Standalone: `guildhall release rollback` sets the pin to the previous version from the `release-pin` events, with source `rollback`.
- Managed: the Steward rolls back by rollout of the previous `managed.toml` revision, which carries the previous `release`, followed by `config reload` and `director reload`.

The previous release is protected from pruning, so rollback never needs a download.

## Pruning

`guildhall release prune` runs automatically after every pin change and can be run by hand.
It keeps the newest `release_keep` installed releases by install time (default 3, a local or managed setting) and removes others only if none of these holds:

- it is pinned;
- it is the rollback target, the previous version in the `release-pin` events;
- a running or resumable task records it, which includes every task not yet `done` or `cancelled`.

So more than `release_keep` releases may stay when tasks still need them.
Each removal is a `release-pruned` store event with the version, its sha256 and the reason it was eligible; a release that was kept past the limit is listed with the reason in `guildhall release status`.
Prune removes only `releases/<version>/` directories and stale `.incoming/` copies, never the launcher, the store or `managed.toml` revisions.

## Commands and store records

| Command | Does |
|---|---|
| `guildhall release install <version> --from <file> --sha256 <hex>` | Verifies and installs a release side by side. |
| `guildhall release install --launcher` | Replaces the PATH launcher from the running release. |
| `guildhall release use <version>` | Standalone: sets the pin, records `release-pin`, then prunes. |
| `guildhall release rollback` | Standalone: moves the pin to the previous version, then prunes. |
| `guildhall release prune` | Removes unprotected releases beyond `release_keep`. |
| `guildhall release status` | Prints the pin, installed releases, which tasks hold each, and any damage or mismatch. |
| `guildhall director reload` | Requests an agent restart at the next step boundary. |

| Store record | Contract |
|---|---|
| Release pin event | New and previous version and sha256, source `use`, `reload`, `rollback` or `guildmaster-approved`, and the time; never changed. |
| Task release | Version and sha256 recorded at `task new`; never changed. |
| Director reload event | `requested` with its mutation id, then `applied` with the old and new release and config digest. |
| Release pruned event | Version, sha256, the time and why it was eligible. |

## Testing

Contract tests use temporary homes and test-built binaries with stamped versions; CI makes no model calls and uses no SSH.
They cover: the launcher on a present, missing, damaged and absent pin and the `release` exception; a pin change leaving an active task on its release; reload applied only at a step boundary; rollback; and every prune protection.
Each runs on a standalone and a managed copy through the shared helper in [director modes](director-modes.md#testing-both-ways).
