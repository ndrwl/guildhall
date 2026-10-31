# `internal/launcher`

## Contract

The package is [the PATH launcher](../releases.md#the-path-launcher), built as its own small program, `cmd/guildhall-launcher`, which `guildhall release adopt` and `guildhall release install --launcher` install as `<home>/bin/guildhall` (`guildhall.exe`) and in place of the `guildhall` on `PATH`, so `runsProgram` and Herdr still see the name `guildhall`.
It is slice 2 of task d127, and slice 3 adds task-aware dispatch.
It loads no configuration; it opens a store only to read a task's recorded release, read-only.

**Selecting the copy.** `SelectCopy(args, getwd)` selects as the CLI does ([copies](../running.md#copies)), through the same [`internal/copydir`](copydir.md):

1. `--copy <dir>` or `--copy=<dir>` (one or two dashes), made absolute; two naming different directories are refused as conflicting;
2. else, when `--store`, `--config` or `--state` is given, no discovery: the copy is the given store's own directory when it holds a copy marker, the store's owning copy, and otherwise none; two `--store` flags naming different files are refused;
3. else copy discovery from the working directory, which finds no copy under a copy's `state/`.

Arguments after `--` are not read.

**Choosing the release.** `Choose(Options)` finds the home with `release.Locate` from the environment alone, `GUILDHALL_HOME` or the platform default, never from its own path, then:

- with a copy whose `release.pin` names an installed, intact release (`Home.Runnable`), that release;
- a pin whose release is missing or damaged refuses, naming the copy, the version, the path checked or the damage and the fix, `guildhall release install <version> --from <file> --sha256 <hex>`, or, for a copy with a `managed.toml`, a rollout; it never falls back to another release;
- a copy with no pin refuses, naming `release.pin` and `guildhall release use <version>` or `guildhall release adopt`, or, managed, `guildhall config reload --copy <dir>`; so does a pin that cannot be read;
- no copy refuses, naming `--copy`.

Recovery dispatch replaces each refusal for `guildhall release ...`, and, when no copy is selected at all, for `help`, `-h`, `--help`, `version` and no command, which read and change nothing: it runs the newest installed, verified release (`Home.Newest`) and says why on stderr, `guildhall: launcher: recovery dispatch: <reason>; running on <version>, the newest installed, verified release`.
With none it refuses, saying it never runs an unverified `.incoming` binary.
Explicit `config reload --copy <dir>` joins recovery dispatch with the managed pin, in slice 5.

**Task-aware dispatch.** `TaskCommand(args)` names the task commands, the ones that advance, launch or resume an existing task, and the task each names:

- by their one positional argument with `--repo`: `task start`, `task cancel`, `task reconcile` and `quality judge`, every flag of theirs taking a value but `task start --ignore-deps`;
- by `--repo` and `--task`: `step done|finish|retry|send-back|park|unpark`, `artisan start|resume`, `quality judged` and `task design`.

A task command missing its repository or task, or naming either twice with different values, is refused before anything else, `<command> names no task: it needs --repo and <the task id|--task>, and a task command is never run as new work`, or `names conflicting tasks`; one given `-h` or `--help` is not a task command.
For a task command `Choose` first reads the task's [recorded release](../store.md#task-releases) with `store.ReadTaskRelease` from `--store`, else the selected copy's `director.store`, read-only, waiting up to ten seconds for the store's lock and without the schema check `store.Open` makes, since the launcher is one fixed program while the store's schema moves on.
A recorded release is run when it is installed, intact and has the recorded sha256, whatever the copy pins and even when it pins nothing; otherwise the command is refused, `task <repo>/<id> records release <version> (sha256 <hex>), and a task is never moved to another release: <why>; install that release with guildhall release install <version> --from <file> --sha256 <hex>`, with no recovery dispatch and no fallback.
A store that cannot be read refuses too; a task with no record, created before its copy recorded releases or on a copy with no pin, an unknown task and a store that does not exist go on to the copy's pin as above, and the release then judges the task.
Herdr's resume argv, `guildhall artisan resume --store … --repo … --task …`, is a task command, so a resume after a restart runs on the task's release, as do the Director's own `task start` and `step` commands and an Artisan launcher's `step done` advance.
Every other command, `task new` included, runs on the pin.

**Running it.** `ChildEnv(env, version)` drops every inherited `GUILDHALL_RELEASE` and `GUILDHALL_LAUNCHER_PROBE` and sets `GUILDHALL_RELEASE=<version>`, so the inherited value is never a selection input.
`Main` prints `guildhall: launcher: <reason>` and exits 1 on a refusal; otherwise:

- on Unix it `exec`s the release with the same arguments and standard streams, so the release is the very process its caller started and recorded;
- on Windows it starts the release as a child in the same console with the launcher's standard streams, ignores Ctrl-C, which the console delivers to the child itself, and exits with the child's exit code.
  The child inherits the launcher's Job Object, the runtime unit a Director or Artisan launch recorded, so that unit still captures the real process; when the launcher is in a job and the child is not, it kills the child and refuses.

Under `GUILDHALL_LAUNCHER_PROBE=1` it prints `{"schema":"guildhall.launcher","version":1}` and dispatches nothing, which is how install and adopt tell a launcher from a release.

## Tests

`launcher_test.go` was written with the launcher and not run red.
The test binary plays both the launcher and, copied into a temporary home's `releases/<version>/` by `Home.Install`, the release, which prints its arguments, `GUILDHALL_RELEASE`, its path and its stdin as JSON and exits with `FAKE_EXIT`.
`TestLauncherRunsTheCopyPin` runs a pinned release by `--copy`, `--copy=`, discovery at and below a copy, a store's owning copy and a `release` command, each with the arguments unchanged and an inherited `GUILDHALL_RELEASE` replaced, with copy A on v1.0.0 and copy B on v2.0.0 each running its own, then forwards stdin and exit code 7 and drops the probe variable.
`TestLauncherRefusesAnUnusablePin` refuses `director start` and `task advance` on a missing, damaged and absent pin, a managed copy's absent pin and no copy, naming the version, path and fix.
`TestLauncherRecoveryDispatch` runs `release status` from an absent, missing and damaged pin and from no copy on the newest verified release past a damaged newer one, and refuses with only an `.incoming` binary.
`TestLauncherRefusesConflictingSelection` refuses two `--copy` and two `--store` flags; `TestLauncherProbe` the probe report; `TestChildEnv` the environment.
`task_test.go` was written with task-aware dispatch in slice 3 and not run red.
`TestLauncherRunsATaskOnItsRecordedRelease` writes a copy pinned to v2.0.0 whose store has a task recorded on v1.0.0 and an unrecorded one, and runs `task start` (with `--repo=`, flags after the id and `--copy` from outside the copy), `step done`, `artisan start`, `launch.ResumeArgv`'s plain resume argv from outside the copy and `quality judge` on v1.0.0, each with the inherited `GUILDHALL_RELEASE` replaced, and the unrecorded and unknown tasks, `task new`, `task show` and `task start --help` on the pin; with the pin removed the recorded task still runs on v1.0.0 and the unrecorded one is refused for no pin.
`TestLauncherRefusesAnUnusableTaskRelease` refuses a task recorded on a release that is missing, damaged or installed with another sha256, naming the version and the fix, and refuses task commands naming no repository, no task, or two tasks.
`TestTaskCommand` is the table of task commands and the task each names.
`launcher_windows_test.go`'s `TestLauncherChildStaysInTheJob`, run only in the Windows CI job, starts the launcher in an `artisan.NewUnit` Job Object, sees the launcher and its release both members while the release blocks on stdin, and gets the release's output and exit code 5 back.
`TestLauncherSurvivesCtrlBreak`, also Windows-only, starts the launcher in a new process group with a release that prints `ready` once it listens for an interrupt (`FAKE_INTERRUPT=1`), sends the group `CTRL_BREAK_EVENT` with `GenerateConsoleCtrlEvent` (a new group has Ctrl-C disabled, and Go delivers both as `os.Interrupt`), and sees the launcher outlive the event and exit with the release's own exit code 9.
`internal/cli`'s `TestCopySelectionIsShared` runs one table through the CLI's copy selection and `SelectCopy`.
