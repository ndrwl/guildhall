# Artisan launcher

The Artisan launcher is the per-attempt process that owns one Artisan worker and every process it starts.
This page describes what is built today: the operating-system seams the launcher stands on, process identity and the containment unit.
The ownership record, control endpoint, stop ladder, heartbeat and the `artisan run`, `stop`, `status` and `resume` commands are later work; nothing here is wired into a command yet.
The package is `internal/artisan`; its doc comments hold the same contract.

## Process identity

A process is named by an `Identity`: its PID plus a creation token that differs for a later process reusing the PID.

| Platform | Token |
|---|---|
| Linux | Boot id and start time in clock ticks since boot, from `/proc` |
| Windows | Creation time from `GetProcessTimes` |
| macOS | Start time from `kern.proc.pid` |

`Lookup` reads the identity of a live process.
`Probe` reports a recorded identity as alive, gone or unverified:

- alive when a live process has the same PID and token;
- gone when no live process has the PID, when the PID now belongs to a process with another token, or when the process has exited and is an unreaped zombie;
- unverified when the probe fails for any other reason, such as a permission error; unverified is never treated as gone.

On Windows a live process that has called `ExitProcess(259)` reads as live, because 259 is the code Windows reports for a running process.

## Containment unit

The launcher starts the worker inside a containment unit, `artisan.Unit`, and stops it only through that unit.
A unit holds one worker and every descendant that stays inside it.

| Operation | Contract |
|---|---|
| `Start(cmd)` | Runs the command inside the unit and returns the worker's identity once membership is verified; a failure after the process exists kills it and returns `ErrContainment`. A second start is `ErrStarted`. |
| `Members()` | The live contained processes by identity. Empty means the unit is empty. A member whose identity cannot be read is an error, never left out. |
| `Interrupt()` | Asks every member to stop; members may ignore it. |
| `Terminate()` | SIGTERM to the group on Unix; refused on Windows with `ErrUnsupportedRung`, which matches `errors.ErrUnsupported`. |
| `Kill()` | Ends every member without their cooperation. |
| `Exited()` | Closed once the worker, the direct child, has exited. |
| `Close()` | Kills what remains, waits for the worker and releases the unit; idempotent; afterwards every other operation is `ErrClosed`. |

`Members` and the stop operations before `Start` are `ErrNotStarted`.
A stop rung counts as successful only when `Members` is empty afterwards; delivering a signal proves nothing.
The methods are safe for concurrent use.

### Windows: Job Object

- One named job, `Local\guildhall-<name>`, per unit; a name already in use is refused rather than joined.
- The only limit is `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so breakaway is not allowed and a `CREATE_BREAKAWAY_FROM_JOB` request is refused.
- The job handle is not inheritable, so only the launcher holds it and its death closes the job.
- The worker is created suspended, assigned to the job, checked with `IsProcessInJob` and only then resumed with `NtResumeProcess`, so no descendant can start outside the job.
- `Interrupt` sends `CTRL_BREAK` to the worker's console process group, which a worker without a shared console does not receive.
- `Members` reads the job's process id list, which is bounded at 512 processes; more is an error.

### Unix: process group

- The worker leads its own process group (`Setpgid`).
- When the worker's stdin is the launcher's terminal, the worker group becomes the terminal's foreground group (`Foreground`, `Ctty`), so Ctrl+C in the pane reaches the worker rather than the launcher; `Close` gives the terminal back.
- The leader is not reaped before `Close`: Linux waits with `waitid(WNOWAIT)` and macOS polls until the leader is a zombie, so the group id stays allocated while the unit can signal it.
- Every signal is preceded by the leader check: a live process at the group id that is not the recorded worker refuses the signal with `ErrGroupReused`, and a leader that cannot be read is not signalled.
- Linux sets `Pdeathsig` to SIGKILL, starting the worker from a locked OS thread because the signal follows the creating thread.

### Limits

These are reported, not guaranteed:

- A Unix descendant that leaves the group with `setsid` or `setpgid` is not a member and survives `Kill`; the contract suite records it as an escape.
- Linux's parent-death signal reaches the worker only, not its descendants; macOS has no parent-death signal.
- macOS is covered by the contract suite on its CI runner only; no interactive macOS run has been made.
- The terminal foreground hand-over is not covered by CI, which has no terminal.

## Tests

`artisantest.RunUnitContract` is the containment contract.
It runs against this platform's backend in `internal/artisan/unit_test.go` on the Linux, Windows and macOS runners, and against `fakeUnit`, the in-memory double for the launcher's tests, in `fakeunit_test.go`.
The test binary plays every process: the worker records its own identity, and a worker tree can include grandchildren, ignore interrupts, start a grandchild with `setsid` (Unix) or ask for breakaway (Windows).

| Case | Every unit | Platform |
|---|---|---|
| The worker and a spawned grandchild are members, with the identities they recorded themselves | yes | |
| An ignored interrupt leaves every member | yes | |
| Kill empties the unit, closes `Exited`, and every owned process is gone | yes | |
| `Exited` closes and the unit empties when the worker ends on its own | yes | |
| Terminate empties the unit | | Unix |
| Terminate is refused with `ErrUnsupportedRung` and members remain | | Windows |
| Close kills what remains, is idempotent and refuses later use | yes | |
| A second start, and members or rungs before start, are refused | yes | |
| A program that cannot start is an error | yes | |
| A `setsid` grandchild is not a member and is alive after kill empties the unit: an escape | | Unix process group |
| A breakaway request is refused and the grandchild is contained | | Windows job |

Every case follows the harness rules: it records every identity it caused and checks them by PID and token after the case, killing only identities it recorded and never by name; an unrelated sentinel process must survive the case; and every wait has a deadline and says what it waited for.
`TestLeaderCheck` drives the Unix rungs with a recorded worker, a reused group id, an exited leader and an unreadable leader without signalling any real process.
`TestIdentityContract` checks the identity rules against live, reused, zombie and reaped processes.
