# Artisan resume sweeps a recorded unit only after the owner-gone rule

`guildhall artisan resume` is L7 part 2's reconcile entry for an attempt whose launcher has exited ([resume](../artisan.md#resume)).
It sweeps the recorded unit only when the recorded launcher identity is proven gone, and settles the attempt only once `reconcile.ProbeOwner`, the one owner-gone rule, proves the unit empty; a live or unproven owner, or a record without containment, is refused and nothing is signalled.
The rule was extracted from `launch.ProbeOwner` and `reconcile.Classify` rather than written a third time (sustainability review 7, F18).
`artisan.Sweep` kills by recorded containment from outside the launcher: a Windows job by name, a Linux cgroup through `cgroup.kill`, and a process group member by member with `artisan.Kill`, which signals only a process whose PID and token still match.
Escapes from a process group are not swept; that limit is the containment's, already reported.
The decision is written with `artisan.Settle` under the attempt lock, with the launcher's precedence and an existing `outcome.json` kept, and the store outcome is recorded in the same lock while the record still names the gone launcher, so a concurrent take-over or resume cannot be finished by mistake.
Resume adds no store record kind: it writes `FinishAttempt` with a mutation id derived from the attempt and the gone launcher, so a rerun records the same outcome; the interpreter's replay matrix covers only the engine's writes and is unchanged.
`launch.ResumeArgv` starts with the plain command name `guildhall`, because Herdr refuses a path as `resume_argv`, and `herdr.Client.ReportAgent` registers it through `pane report-agent --source guildhall`; calling it from the pane belongs to the slice that runs the launcher in the Herdr tab.
The fake Artisan gained the P0 lifecycle directives inside `internal/adapter/fake` rather than a second fake, honours the `.cancelled` sidecar, and plays the stop contract's worker through `artisantest.Harness.Worker`.
The lab harness commands sit under `guildhall artisan harness` and refuse to run without `--lab`, because they are test tooling.
L7 part 3 holds the Tier 2 lab runs on WSL and the Windows pilot, the Herdr tab mode running the launcher in its pane, and the retirement of the Windows-only worker stop path once the ladder passes the native Windows case.
