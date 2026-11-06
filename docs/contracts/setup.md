# `internal/setup`

## Contract

The setup engine ([setup](../setup.md), [decision](../decisions/setup-engine-from-firstmate.md)).

- `Parse(data, wantDigest)`, `LoadSteps(path, wantDigest)` and `LoadBuiltin(name, wantDigest)` return `Steps`: a strict `guildhall.steps/1` file with its `Digest` (lowercase hex sha256 of its bytes), `Ref` (a built-in's name or the path) and steps. A non-empty `wantDigest` that differs is `ErrDigestMismatch`; a file that does not follow the format is `ErrInvalid`; an unknown built-in is `ErrUnknownStep`. `Builtins()` lists the embedded files and `BuiltinBytes(name)` returns one.
- `Doctor(ctx, Steps, Facts) (Report, error)` runs each check for `Facts.GOOS` and returns a `guildhall.doctor-report/1` report: every check, each failing one as a `Gap{ID, Title, Tag, Action, Detail}` whose `ID` is the step id and `Action` the exact `guildhall setup` command that closes it, and the steps skipped for another OS. It writes nothing.
- `Fix(ctx, Steps, Facts, ids, Journal) (Report, error)` acts on the fixable steps `ids` names (all of them when empty), then returns a fresh `Doctor` report. A named human step is `ErrHumanStep`, an unknown one `ErrUnknownStep`, one for another OS `ErrUnsupportedOS`, each before anything runs. A failed act or verify stops the fix with a `*StepError{Step, Err}`, and the report is still the fresh one.
- `Run(ctx, Steps, Facts, ids, Journal) (Outcome, error)` acts on fixable steps in order and returns `Outcome{Waiting, Action}` at the first human step whose check fails.
- `Wizard(ctx, Steps, Facts, WizardOptions{IDs, DryRun}, Prompter, Journal) (Record, error)` carries out each chosen step with the person, writes the `guildhall.wizard-record/1` record at `RecordPath(setupDir, name, digest)` and returns it; a failed step shows its undo note and returns a `*StepError` with the record marked failed. `DryRun` runs, asks and writes nothing. `Prompter` (`Show`, `Confirm`, `Ask`, `AskSecret`) is an interface so tests answer without a terminal; `TermPrompter` is the terminal one, reading a secret with echo off.
- `Facts` carries the setup directory (required, absolute, for every call but `Doctor`), the home directory, the caller's values for the file's params (an undeclared one is `ErrInvalid`), the platform, the streams a human step's `run` is attached to, the HTTP client and the clock. This adds `Facts` and `Journal` to the signatures d128's spec sketched (`Fix(ctx, Steps, ids)`, `Wizard(ctx, Steps, ids, Prompter, Journal)`), since every act needs the setup directory and values.
- Errors: `ErrDigestMismatch`, `ErrUnknownStep`, `ErrNotOwned` (an existing file a step's `creates` or `fetch` names, outside the manifest), `ErrHumanStep`, `ErrUnsupportedOS`, `ErrChecksum` (a download whose sha256 is not the named one; nothing is installed), `ErrDeclined` (a confirm answered no) and `ErrInvalid`.
- Ownership: the engine owns its setup directory, its `journal.jsonl`, `records/` and `manifest.json`, and the files the manifest lists; `ReadManifest` and `ReadRecord` read them back.
- Idempotency: every step's check runs first, and a passing check skips the act; verify is a fresh run of the check unless the step names its own.
- Secrets: an `ask_secret` value reaches only the step's `run` command, as `SETUP_<NAME>`; the journal, record and returned errors never hold it.
- Commands run with no shell unless a step names `sh` or `pwsh` with a script; a check is bounded at two minutes and an act at thirty.

`guildhall setup doctor|wizard|run` in `internal/cli` is the command line over it; a step file named by path needs `--digest`.

## Tests

`internal/setup`'s tests run on every CI runner (Linux, macOS, Windows) on a temporary `HOME`, with copies of the test binary as the stub `setuptool`, `git`, `claude` and `herdr`, so no shell is needed:

- `TestDoctorReport`: the gap ids, tags and action lines, the skipped step, nothing written, and the same report twice.
- `TestFixOnlyTouchesNamedGap`, `TestFixAllClosesOnlyFixableGaps`, `TestFixRefusals`: `--fix --only` touches only the named gap and re-derives every check; a human, unknown or other-OS step, a missing setup directory and an undeclared value are refused with nothing run.
- `TestNotOwned`: an existing file outside the manifest is `ErrNotOwned` and is left as it was; a file the engine created may be rewritten.
- `TestRunWaitsAtHumanStep`, `TestWizardRunsFixableAndSkipsDone`: the run stops at the human step; the wizard's results and journal.
- `TestWizardDryRunChangesNothing`, `TestWizardShowsUndoOnFailure`, `TestWizardKeepsNoSecret`: dry run writes nothing; numbered cards and the undo note on failure; no `ask_secret` value in the journal, record or error, even when the run command prints it.
- `TestDigestMismatch`, `TestParseRefusals`, `TestBuiltins`: one byte changed is `ErrDigestMismatch`; malformed files are `ErrInvalid`; the seven built-ins parse with unique step ids and their human steps tagged human.
- `TestBuiltinNewCopy`, `TestBuiltinGuildhallFacts` on Linux and macOS: `new-copy`'s gaps for a config or store in the copy directory and a clone path that is a file or not empty; `director`'s `guildhall-pin`, `guildhall-on-path` and `tailscale-ssh` and `herdr`'s `herdr-session` against stub `guildhall`, `tailscale` and `herdr` programs, each passing with no value given.
- `TestFetchRefusesWrongChecksum`, `TestUnpackRefusesEscapes` on every OS, and `TestBuiltinHerdr` and `TestBuiltinGoUnpacks` on Linux and macOS: a wrong checksum installs nothing, a tar entry that leaves its destination is refused, and the built-in `herdr` and `go` steps install from a local server and are owned in the manifest.

`internal/cli`'s `TestSetupDoctor`, `TestSetupRefusals`, `TestSetupWizardDryRunAndRun` and `TestSetupDoctorAllBuiltins` cover the commands' output, exit codes and refusals.
These tests were written with the package; they were not run red against a stub.
