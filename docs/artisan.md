# Artisan launcher

The Artisan launcher is the per-attempt process that owns one Artisan worker and every process it starts.
This page describes what is built today: the operating-system seams the launcher stands on, process identity and the containment unit.
The [ownership record](store.md#attempt-ownership) is in the store; the control endpoint, stop ladder, heartbeat and integration into launcher commands are later work, and nothing here is wired into a command yet.
The existing fake-adapter `artisan run` command is described in [adapters](adapters.md#running-an-attempt).
The package is `internal/artisan`; its [doc comments](../internal/artisan/artisan.go) own the API contract.

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

## Containment unit

The launcher starts the worker inside a containment unit, `artisan.Unit`, and stops it only through that unit.
A unit holds one worker and every descendant that stays inside it.

| Operation | Contract |
|---|---|
| `Start(cmd)` | Runs the command inside the unit and returns the worker's identity once membership is verified; a failure after the process exists kills it and returns `ErrContainment`. A second start is `ErrStarted`. |
| `Members()` | The live contained processes by identity. Empty means the unit is empty. A member whose identity cannot be read is an error, never left out. |
| `Interrupt()` | See the [`Unit.Interrupt` contract](../internal/artisan/artisan.go), including Windows delivery limits. |
| `Terminate()` | SIGTERM to the group on Unix; refused on Windows with `ErrUnsupportedRung`, which matches `errors.ErrUnsupported`. |
| `Kill()` | Ends every member without their cooperation. |
| `Exited()` | Closed once the worker, the direct child, has exited. |
| `Close()` | Cleanup and retry semantics are defined in the [`Unit.Close` contract](../internal/artisan/artisan.go). |
| `Containment()` | Which backend contains the unit, fixed at creation: its kind, the name a later sweep finds it by, and on Linux the detection reason. |

`Members` and the stop operations before `Start` are `ErrNotStarted`.
A stop rung counts as successful only when `Members` is empty afterwards; delivering a signal proves nothing.
The methods are safe for concurrent use.

### Windows: Job Object

- One named job, `Local\guildhall-<name>`, per unit; a name already in use is refused rather than joined.
- The only limit is `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, so breakaway is not allowed and a `CREATE_BREAKAWAY_FROM_JOB` request is refused.
- The job handle is not inheritable, so only the launcher holds it and its death closes the job.
- The worker is created suspended, assigned to the job, checked with `IsProcessInJob` and only then resumed with `NtResumeProcess`, so no descendant can start outside the job.
- Console signalling follows the [`Unit.Interrupt` contract](../internal/artisan/artisan.go).
- `Members` reads the job's process id list, which is bounded at 512 processes; more is an error.

### Recording the containment

`RecordContainment(dir, c)` writes `containment.json` in the attempt directory, replacing any earlier record atomically:

```json
{
  "containment": "cgroup",
  "name": "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice/pane.scope/guildhall-<name>",
  "reason": "cgroup v2 delegated by the systemd user manager at /user.slice/user-1000.slice/user@1000.service/app.slice/pane.scope (WSL)"
}
```

| Kind | Backend | Name |
|---|---|---|
| `job` | Windows Job Object | `Local\guildhall-<name>` |
| `cgroup` | Linux child cgroup of a delegated cgroup v2 | The cgroup directory |
| `subreaper+pgroup` | Linux process group with the launcher as child subreaper | none |
| `pgroup` | macOS process group | none |

### Linux: detection

A Linux unit uses the cgroup backend only when all of these hold, and otherwise falls back to the subreaper and process group with the first failed check as its reason:

1. `/sys/fs/cgroup` is a cgroup v2 mount (the topmost mount there in `/proc/self/mountinfo`); cgroup v1 and the hybrid layout fall back.
2. `/proc/self/cgroup` has a cgroup v2 entry inside the mounted subtree.
3. This user may write the launcher's own cgroup directory and its `cgroup.procs`, as in a subtree delegated by the systemd user manager (`user@<uid>.service`).
4. The cgroup has `cgroup.kill` (Linux 5.14).

No controller is enabled, so `cgroup.subtree_control` is not needed.
The reason names a systemd user manager subtree and a WSL kernel when it sees them: WSL with systemd usually runs panes in a delegated user scope, and WSL without systemd usually runs them in the root cgroup, which a user cannot write.
A cgroup that detection allowed but that cannot be created is an error, not a fallback.

### Linux: cgroup

- `newUnit` creates `guildhall-<name>` inside the launcher's own cgroup; a name already in use, empty, `.`, `..` or containing `/` is refused rather than joined.
- The worker is created inside it with `CLONE_INTO_CGROUP`, so no descendant can start outside it, and also leads its own process group with the terminal hand-over below.
- `Start` verifies that the worker's `/proc/<pid>/cgroup` names the unit's cgroup before reporting it running.
- `Members` reads `cgroup.procs`, so a descendant that calls `setsid` is still a member.
- `Kill` writes `cgroup.kill`; `Interrupt` and `Terminate` signal every process in the cgroup through a pidfd taken before its membership is re-checked, so a reused PID is never signalled.
- `Close` removes the emptied cgroup, retrying while exiting processes leave it; a cgroup that cannot be removed keeps the unit open so `Close` can be retried.

### Linux: subreaper fallback

- The launcher process makes itself the child subreaper (`PR_SET_CHILD_SUBREAPER`) once, then contains the worker in a process group as below.
- A descendant that leaves the group and is orphaned reparents to the launcher instead of init.
- `Escapes` on the fallback unit lists those live reparented processes; the launcher runs one unit and starts no other children, so each is attributed to its unit.
- Escapes are reported, not contained: they are not members and `Kill` does not reach them.
- A reparented escape that exits stays a zombie of the launcher until the launcher exits; a zombie is gone by the identity rules.

### Unix: process group

The macOS backend and the Linux fallback.

- The worker leads its own process group (`Setpgid`).
- When the worker's stdin is the launcher's terminal, the worker group becomes the terminal's foreground group (`Foreground`, `Ctty`), so Ctrl+C in the pane reaches the worker rather than the launcher; `Close` gives the terminal back.
- The leader is not reaped before `Close`: Linux waits with `waitid(WNOWAIT)` and macOS polls until the leader is a zombie, so the group id stays allocated while the unit can signal it.
- Every signal is preceded by the leader check: a live process at the group id that is not the recorded worker refuses the signal with `ErrGroupReused`, and a leader that cannot be read is not signalled.
- Linux sets `Pdeathsig` to SIGKILL, starting the worker from a locked OS thread because the signal follows the creating thread.

### Limits

These are reported, not guaranteed:

- A Unix descendant that leaves the group with `setsid` or `setpgid` is not a member of a process-group unit and survives `Kill`; the contract suite records it as an escape, and the Linux fallback reports it through `Escapes` once it is orphaned.
- When the launcher dies, the Linux parent-death signal kills the worker only; descendants in a cgroup unit remain in its named cgroup.
- Linux's parent-death signal reaches the worker only, not its descendants; macOS has no parent-death signal.
- macOS is covered by the contract suite on its CI runner only; no interactive macOS run has been made.
- Interactive terminal foreground hand-over is not covered by CI; the Linux failed-exec restoration case uses a pseudo-terminal (see the [package test inventory](contracts/artisan.md#tests)).

## Tests

`artisantest.RunUnitContract` is the containment contract.
It runs against this platform's backend in `internal/artisan/unit_test.go` on the Linux, Windows and macOS runners, and against `fakeUnit`, the in-memory double for the launcher's tests, in `fakeunit_test.go`.
On Linux it also runs against each backend by name in `unit_linux_test.go`: the subreaper fallback on every host, and the cgroup backend when detection finds a delegated cgroup v2, skipped with the detection reason when not.
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
| A `setsid` grandchild is a member and kill ends it | | Linux cgroup |
| The unit reports its containment kind | yes | |
| A breakaway request is refused and the grandchild is contained | | Windows job |

Every case follows the harness rules: it records every identity it caused and checks them by PID and token after the case, killing only identities it recorded and never by name; an unrelated sentinel process must survive the case; and every wait has a deadline and says what it waited for.
`TestLeaderCheck` drives the Unix rungs with a recorded worker, a reused group id, an exited leader and an unreadable leader without signalling any real process.
`TestIdentityContract` checks the identity rules against live, reused, zombie and reaped processes.
`TestDetectCgroup` is the detection table over recorded host files: cgroup v2 delegated by a systemd user manager, on WSL and inside a cgroup namespace; cgroup v2 not delegated as a system service, on WSL without systemd and with `cgroup.procs` locked; no `cgroup.kill`; a cgroup outside the mounted subtree; cgroup v1; hybrid; no cgroup filesystem; unreadable mountinfo.
`TestCgroupKillEmpties` shows `cgroup.kill` ending a grandchild that left the process group and `Close` removing the cgroup; `TestSubreaperRecordsReparent` shows an orphaned `setsid` grandchild reparented to the launcher and reported by `Escapes`, not `Members`.
