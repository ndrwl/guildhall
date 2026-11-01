# `internal/release`

## Contract

The package owns the Guildhall home of [release upgrades](../releases.md): the releases installed side by side under it, the inventory of copies that use them and the home lock.
Slice 1 of task d127 built it; slice 2 adds the pin file and what the [launcher](launcher.md) reads, slice 3 task creation under the home lock and release contract 2, and slice 4 pruning.

**The home.** `Locate(getenv, exe, version, goos)` finds it, in this order:

1. `GUILDHALL_HOME` (`HomeEnv`) when set, made absolute;
2. when `exe`, the running binary, is `<home>/releases/<version>/guildhall` (`guildhall.exe` on Windows) for its own version, after resolving symlinks, that `<home>`, so an installed release finds its home with nothing set;
3. the platform default: `%LOCALAPPDATA%\Guildhall` on Windows, and elsewhere, WSL and a Linux VDS alike, `$XDG_DATA_HOME/guildhall` when that is absolute, else `$HOME/.local/share/guildhall`.

With none of these it returns `ErrNoHome`, naming the variables to set.
`Home` holds the absolute directory and the platform whose binary name its releases use (`BinaryName`), and names the layout's paths: `bin/`, `releases/<version>/`, `releases/.incoming/`, `copies` and `lock`.

**Release versions.** `ValidVersion` admits a `v`, then a letter or digit, then letters, digits, `.`, `-` and `+`, with no `..`, so a version is always one plain directory name that cannot be `.incoming`.
It refuses `dev`, the version of a plain `go build`, which different builds share ([d127 grill Q1](../releases.md)).
Tagged versions (`v1.4.0`) and main pseudo-versions (`v0.0.0-20261031162214-d385b331abcd`) are releases.

**A release directory** holds the binary, `SHA256` (its lowercase hex sha256 and a newline) and `release.json`: schema `guildhall.release` version 1, the release, its sha256, the `store_schema` and `release_contract` the binary reported at install, and `installed_at` (RFC 3339 UTC, to the second).
`Check(version)` verifies one: it is intact only when the binary hashes to `SHA256` and `release.json` has the right schema and names the same release and sha256.
Anything else, including any missing piece, an entry that is not a directory or a name that is not a release version, is `Damage`, a reason; a damaged release is never run.
`Check` returns `fs.ErrNotExist` when there is no entry of that name.
`List` checks every entry of `releases/` except dot entries such as `.incoming`, newest install first, then by version, with damaged entries whose time is unreadable last; with no `releases/` it is empty.

**Install.** `Install(ctx, version, from, sha256, probe, now, lockWait)`:

1. refuses an invalid version and a sha256 that is not 64 hex digits, which it lowercases;
2. reports a release of that version already intact with that sha256 as `Already`, changing nothing and running nothing; refuses one installed with another sha256 or damaged, since a release directory is never changed;
3. copies `from` into a new directory under `releases/.incoming/`, mode 0755 and synced, hashing as it copies, and refuses a sha256 other than the expected one;
4. only then runs the verified copy through `probe`, `guildhall version --json`, and refuses a report of `dev`, of another version, of `modified: true`, or with no `release_contract` (below 1, a binary that predates release installs);
5. writes `SHA256` and `release.json` beside it;
6. under the home lock checks step 2 again and renames the directory to `releases/<version>` in one step, then checks it.

Every refusal leaves `releases/` unchanged and removes the `.incoming` copy.
An unverified `.incoming` binary is never run; the probe runs only after its sha256 matched.

**The inventory.** `Copies` reads `copies`, one absolute copy directory per line; a home without one has none.
`Register(dir, lockWait)` adds `dir`, made absolute and clean, under the home lock, keeping the file sorted and without duplicates and publishing it with [`atomicfile`](atomicfile.md); registering a copy already there changes nothing, and a directory containing a line break is refused.
`RegisterLocked(dir)` is `Register` for a caller already holding the home lock: `release use` and `release adopt` register their copy and record its pin under one hold, and `task new` on a copy with a pin registers it and records the task with its release under one hold.

**The pin.** `PinFile` is `release.pin`, a copy's pinned version on one line, a cache of its newest `release-pin` store event ([the pin](../releases.md#the-pin)).
`ReadPin(dir)` reads it, `fs.ErrNotExist` for a copy with none, and refuses one that is not a valid release version; `HasPin(dir)` is true for any pin file, readable or not, the mark of a copy that has adopted; `WritePin(dir, version)` writes a valid version with [`atomicfile`](atomicfile.md).
`Newest()` is the newest intact release by install time, for the launcher's recovery dispatch, and `Runnable(version)` the release when it is installed and intact, else an error naming the missing path or the damage.
`Launcher()` is `bin/guildhall` (`guildhall.exe`), and `LauncherName(goos)` the launcher's build name, `guildhall-launcher` (`.exe`).
`Env` is `GUILDHALL_RELEASE`, which the launcher sets for the release it runs; `LauncherProbeEnv` (`GUILDHALL_LAUNCHER_PROBE=1`) makes a launcher print its `LauncherReport`, schema `guildhall.launcher` version 1, instead of dispatching.
`RunningFrom(path, self)` lists, on Windows, the processes other than `self` whose image is `path`, which `release adopt` must not replace while they run; elsewhere it is none, as a running executable's file can be replaced.

**The home lock.** `Lock(wait)` creates the home and its `lock` file when missing and takes an exclusive lock on it, `flock` on Unix and `LockFileEx` on Windows, polling every 20 ms for up to `wait`, then returning `ErrLocked`.
The operating system releases it when the process ends, however it ends.
It is per open file, so a second `Lock` in the same process waits like another process's.
Install's rename, `Register`, pin changes, task creation and pruning hold it, so a prune's reference checks and removals are serialized with every way a reference is made.

**Pruning** ([pruning](../releases.md#pruning)). `Keep` is 3, fixed, not a setting.
`PlanPrune(releases, held)` takes the releases as `List` orders them and the holds by version, and keeps the first `Keep` and every older one with a hold, listing its holds as `Held`; each other is `Remove`, with a `Reason` saying it is older than the newest three and held by nothing, prefixed with its damage for a damaged one.
`RemoveLocked(version)`, for a caller holding the home lock, renames the release directory to a new `.prune-<version>-*` directory under `.incoming/`, so it leaves `releases/` in one step and is never seen half removed, then removes that.
`PruneIncomingLocked(now)` removes each `.incoming/` entry last changed more than `IncomingStaleAfter`, 24 hours, before `now`, and any `.prune-*` a failed removal left, returning their names; a fresh install's or rollout's copy stays.
The callers in [the CLI](cli.md#release) gather the holds from every inventoried copy and refuse when one cannot be read.

`Contract` is the release contract this build implements, which `guildhall version --json` reports as `release_contract`: 1 was the home, install and status, and 2, from slice 3, adds tasks recording their release at `task new` and running on it.

## Tests

`release_test.go` was written with the implementation and not run red.
`TestLocate` covers each source in order, a relative `GUILDHALL_HOME`, a release under another version's directory, a relative `XDG_DATA_HOME` and no home on Linux and Windows; `TestValidVersion` the names admitted and refused.
`TestInstall` installs through a recording probe, which runs only the copy under `.incoming`, then installs again as `Already` with nothing run, and refuses another binary under the same version; `TestInstallRefusals` refuses `dev`, a bad name or sha256, a sha256 mismatch with no probe run, a report of `dev`, another version, a modified build, no release contract, a failing probe and a missing file, each leaving no release and an empty `.incoming`.
`TestCheckFindsDamage` changes or removes each piece of an installed release, sees `Check` name it and `Install` refuse over it; `TestList` the order and a stray entry reported damaged.
`pin_test.go` was written with slice 2 and not run red: `TestPin` the missing, written, read and refused pin files; `TestNewestAndRunnable` the newest install, skipping a damaged one, and the missing and damaged refusals.
`prune_test.go` was written with slice 4 and not run red: `TestPlanPrune` keeps the newest three and an older held release with its holds and removes the rest, a damaged one's reason naming its damage, and leaves entries not named as release versions, a stray one and `dev`, uncounted and never removed (slice 4 review f1); `TestRemoveAndPruneIncoming` removes a release, leaving the others listed, refuses a path, and removes a stale and a half-removed `.incoming/` entry, keeping a fresh one.
`TestRegister` covers sorting, duplicates and a cleaned path; `TestRegisterIsSerialized` registers sixteen copies at once and keeps every one; `TestLock` sees a second lock and a `Register` refused with `ErrLocked` while it is held, and taken again after unlock.
