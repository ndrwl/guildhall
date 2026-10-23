# Artisan launcher

The Artisan launcher is the per-attempt process that owns one Artisan worker and every process it starts.
This page describes what is built today: the operating-system seams the launcher stands on, process identity, the containment unit, the control endpoint, the stop ladder, the heartbeat and reconcile's classification.
The [ownership record](store.md#attempt-ownership) is in the store, and the `artisan start`, `stop`, `status` and `resume` [commands](#commands) wire the pieces together.
The Herdr tab mode of `artisan start` is described in [adapters](adapters.md#running-an-attempt).
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
| `Start(cmd)` | Runs the command inside the unit and returns the worker's identity once membership is verified, or once it is found already exited; a failure after the process exists kills it and returns `ErrContainment`. A second start is `ErrStarted`. |
| `Members()` | The live contained processes by identity. Empty means the unit is empty. A member whose identity cannot be read is an error, never left out. |
| `Interrupt()` | See the [`Unit.Interrupt` contract](../internal/artisan/artisan.go), including Windows delivery limits. |
| `Terminate()` | SIGTERM to the group on Unix; refused on Windows with `ErrUnsupportedRung`, which matches `errors.ErrUnsupported`. |
| `Kill()` | Ends every member without their cooperation. |
| `Exited()` | Closed once the worker, the direct child, has exited. |
| `Close()` | Cleanup and retry semantics are defined in the [`Unit.Close` contract](../internal/artisan/artisan.go). |
| `Containment()` | Which backend contains the unit, fixed at creation: its kind, the name a later sweep finds it by, and on Linux the detection reason. |

`NewUnit(name)` returns an unstarted unit of this platform's backend.
`Contained(containment, worker)` lists the live processes of a recorded unit from outside the launcher that created it, for reconciling an attempt whose launcher is gone; its [API contract](../internal/artisan/artisan.go) defines missing-unit and process-group identity handling.
An empty list proves the unit empty; an error, including a kind the platform does not have (`errors.ErrUnsupported`), proves nothing.

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

The store's ownership record keeps the same description, with the worker identity in place of a name for a process group ([containment record](store.md#containment-record)).
A job or cgroup exists, named, from the moment the unit is created, so `artisan start` records it with the ownership, before the worker starts, and the `running` update adds the worker identity; a process group is recorded only once its worker runs.

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

- `NewUnit` creates `guildhall-<name>` inside the launcher's own cgroup; a name already in use, empty, `.`, `..` or containing `/` is refused rather than joined.
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
- A worker that has already exited when `Start` looks up its group has exited, not escaped: the kernel made it the leader of the group at its own pid, so `Start` records its identity, read from the unreaped leader, and reports it started, and the launcher reads the exit as any exit is; `ErrContainment` is kept for a unit that could not be created or a worker that is live and outside its unit.
- The leader is not reaped before `Close`: Linux waits with `waitid(WNOWAIT)` and macOS polls until the leader is a zombie, so the group id stays allocated while the unit can signal it.
- Every signal is preceded by the leader check: a live process at the group id that is not the recorded worker refuses the signal with `ErrGroupReused`, and a leader that cannot be read is not signalled.
- Linux sets `Pdeathsig` to SIGKILL, starting the worker from a locked OS thread because the signal follows the creating thread.

### Limits

These are reported, not guaranteed:

- A Unix descendant that leaves the group with `setsid` or `setpgid` is not a member of a process-group unit and survives `Kill`; the contract suite records it as an escape, and the Linux fallback reports it through `Escapes` once it is orphaned.
- When the launcher dies, the Linux parent-death signal kills the worker only; descendants in a cgroup unit remain in its named cgroup.
- macOS has no parent-death signal.
- A process-group unit (macOS, the Linux fallback) is found by its worker, which is the group id, so it is recorded only once the worker runs: a launcher gone between taking ownership and recording its worker leaves an owner in `starting` with no containment, which `status` calls `exited` but `resume` and `start` refuse as unverified, and `store doctor` counts as starting without containment.
  A job or cgroup is recorded with the ownership and has no such window.
- macOS is covered by the contract suite on its CI runner only; no interactive macOS run has been made.
- Interactive terminal foreground hand-over is not covered by CI; the Linux failed-exec restoration case uses a pseudo-terminal (see the [package test inventory](contracts/artisan.md#tests)).

## Control endpoint

Each owned attempt has one control endpoint: an AF_UNIX stream socket under the Guildhall state directory, on Windows 10 and later as on Unix ([decision](decisions/0034-attempt-endpoint-af-unix.md)).
The launcher binds it, records its path in the ownership record with `UpdateOwnership`, and answers `status`, `stop` and `cancel` on it; what `stop` and `cancel` do is the launcher's handler, the [stop ladder](#stop-ladder).
It is a consumer of Guildhall's one [endpoint](contracts/endpoint.md), which owns binding, the stale-file rule, permissions, strict decoding, the peer check and removal; this section states what the attempt endpoint adds: its path, its ownership check, its owner and its operations.

### Path

`EndpointPath` gives `<state>/endpoints/<repository>.<task>.<step>.<attempt>.sock`, from the absolute state directory and the attempt key alone.
An AF_UNIX path holds at most 103 bytes on macOS and 107 on Linux and Windows (`MaxEndpointPath`).
A longer readable path falls back to `<state>/endpoints/<32 hex digits>.sock`, the first 16 bytes of the SHA-256 of `<repository>/<task>/<step>/<attempt>`; a state directory too long for that is refused with `ErrEndpointPath`.

### Binding

`Listen` binds only when the attempt's ownership record names this launcher and is not released; otherwise it returns `ErrNotOwner` and creates nothing.
Binding, leftover files, permissions and `Close` follow the [shared endpoint contract](contracts/endpoint.md).
`ErrEndpointAnswers` and `ErrEndpointUnknown` alias its `ErrAnswers` and `ErrUnknown` respectively; taking the ownership record does not authorize removing a non-socket file.

### Protocol

The protocol is version 1 of `guildhall.attempt-control` ([decision](decisions/0038-endpoint-protocol-v1.md)).
A request is one JSON line of at most 4 KiB containing `"schema": "guildhall.attempt-control/1"`, `op`, the expected `attempt` string, and the expected `launcher` identity (PID and token):

```json
{"schema":"guildhall.attempt-control/1","op":"stop","attempt":"app/t-1/build/1","launcher":{"pid":4242,"token":"<boot id>:<start time>"}}
```

The endpoint decodes the request strictly with [`strictjson`](contracts/strictjson.md) and refuses, with an error reply and without matching identities or reaching any handler:

| Request | Reply error |
|---|---|
| A newer version, such as `guildhall.attempt-control/2` | Unsupported protocol version, naming version 1 |
| No `schema`, as a client before version 1 sent, or one that is not `guildhall.attempt-control/<n>` | Malformed request |
| A duplicate key, such as two `op` keys, at any depth | Malformed request |
| A field version 1 does not declare, at the top level or inside `launcher`, compared exactly, case included | Malformed request |
| A `launcher` with an incompatible JSON type, such as a string or list, or a string `pid` | Malformed request |
| Invalid JSON or UTF-8, or no newline within the 4 KiB request budget | Malformed request |

Unversioned requests are refused rather than supported during a transition: no client of an earlier protocol existed when the endpoint was wired into a command.
A newer version is refused rather than guessed at, because stop and cancel are destructive; serving a later version means naming it in the endpoint.
Before dispatching status, stop or cancel, the endpoint checks that both identities match its bound attempt and launcher; missing or mismatched identities receive an error without invoking any handler.
The reply is one JSON line carrying `"schema": "guildhall.attempt-control/1"`, including when refusing a request, and naming the attempt and the launcher, with either the launcher's `state` (its phase and, when listed, the contained members) or an `error`.
An unknown operation gets an error reply without reaching the handler.
Requests are answered concurrently, so a `stop` that waits for the attempt to end does not delay a `status`.
`Call` uses the [shared endpoint's transport and context bounds](contracts/endpoint.md) and reads a newline-terminated reply of at most 1 MiB; read or framing failures return an exchange error.
Once a complete line is read, it is decoded with the same strict rules: a reply that is malformed, unversioned, carries an unknown or duplicate field or is a newer version is `ErrEndpointProtocol` (a newer one also matches `strictjson.ErrNewerSchema`).
It refuses a reply naming another attempt or launcher with `ErrEndpointMismatch`; an error reply is `ErrEndpointRequest`.
A failed call never proves the owner exited: a silent endpoint is unknown until the owner's identity is proven gone.

## Stop ladder

A `Launcher` runs one attempt's worker in its unit, decides the attempt once and stops the worker tree ([decision](decisions/0041-stop-ladder-precedence.md)).
It is the endpoint's handler, and its [doc comments](../internal/artisan/launcher.go) own the API contract.
It reads the attempt's result and writes its cancel marker through `AttemptFiles`, and writes `outcome.json`, `events.jsonl` and `attempt.lock` in the attempt directory.
The real `AttemptFiles` is `launch.Files`: the adapter's result file parsed with `adapter.ParseResult`, bound to the attempt identity and version-checked, and the adapter's `.cancelled` sidecar as the marker; launcher and `reconcile.Classify` both read through it, so marker and result have one reader each.
Every `AttemptFiles` adapter reads the result through the bounded [publication-race retry](contracts/atomicfile.md), because a read error decides the attempt failed; the shared stop suite holds that on Windows.

### Precedence

Every decision is taken under the attempt lock, in this order:

1. A cancel marker present: cancelled, and a result valid when the outcome is written, published with or after the marker, is rejected as late (`late_result_rejected`).
2. A valid result: finished, and a stop that arrives after it does not write the marker.
3. Otherwise a stop writes the marker and the attempt is cancelled, and a worker that exits without either has failed.

A marker or result already present, or a stop requested, before the worker starts decides the attempt without starting it, and an attempt that already has `outcome.json` is refused with `ErrDecided`.
`outcome.json` is written once, `"schema": "guildhall.attempt-outcome/1"`, with the state, a detail, the rung that emptied the unit (0 when none was needed) and any survivors:

```json
{
  "schema": "guildhall.attempt-outcome/1",
  "state": "cancelled",
  "detail": "stopped",
  "late_result_rejected": true,
  "rung": 1
}
```

`ReadOutcome` decodes the file with [`strictjson`](contracts/strictjson.md) and never guesses: no file matches `fs.ErrNotExist`, a newer schema matches `strictjson.ErrNewerSchema`, and an unversioned or malformed file, or one whose state is not `finished`, `cancelled` or `failed`, is `ErrInvalidOutcome`.

### Rungs

| Rung | Action | Bound (`DefaultLadder`) |
|---|---|---|
| 1 | The cancel marker, which a cooperating worker honours by exiting | 3 s |
| 2 | `Unit.Interrupt`: SIGINT to the group, `CTRL_BREAK` on Windows | 3 s |
| 3 | `Unit.Terminate`: SIGTERM to the group; skipped on Windows, where it is refused | 3 s |
| 4 | `Unit.Kill`, or terminating the job on Windows | 5 s |

During escalation, a rung succeeds only when the unit is empty within its bound; otherwise the next rung runs, and after rung 4 the members still alive are the outcome's survivors and `Run` and `Stop` return `ErrNotEmpty`.
Marker-only cancellation instead continues cooperative supervision as described under [stop requests](#stop-requests).
An explicit stop of a cancelled attempt climbs from rung 1; a finished or failed attempt whose tree is still running, or a worker outliving its accepted result by the result grace (3 s), climbs from rung 2.
For a started worker, a stop that writes or finds the cancel marker records rung 1 even if the tree empties before the ladder's first check: the marker itself is the rung. Attempts stopped before starting, and empty trees needing no ladder rung, retain rung 0.
The [event table](#events) describes the rung observations recorded in `events.jsonl`.

### Events

The launcher appends one JSON object per line to `events.jsonl`; every line carries `event`, its name, and `at`, an RFC 3339 UTC time, plus the fields below.
An event is observation only; a reader must tolerate unknown events.
The file carries no schema field.
Contract tests already read it, but no command reads it yet; it gains a schema when a command reads it.

| Event | When | Fields |
|---|---|---|
| `stop-before-start` | A stop is requested before the worker starts | |
| `running` | The worker has started in its unit | `worker` |
| `result-accepted` | Polling observes a valid result and decides the attempt finished; startup, stop and worker-exit acceptance do not emit this event | |
| `worker-outlived-result` | The worker is still alive a result grace after its result | |
| `worker-exited` | The worker exits while the launcher supervises it | |
| `signal-forwarded` | Forwarding an interrupt to the worker has been attempted; `error` records any failure | `signal`, `error` |
| `signal` | A stop signal to the launcher requests the ladder | `signal` |
| `decide-failed` | Reading or writing the attempt files fails while deciding | `error` |
| `cancel-marker` | Stop or Cancel is about to attempt writing the cancel marker; this does not confirm success | |
| `stop-after-decision` | A stop arrives after the attempt is decided other than cancelled | `state` |
| `ladder` | The ladder starts, or cooperative supervision begins | `first` |
| `rung` | A rung runs | `rung`, `name`, `deadline`; explicit ladder rungs (including rung 1) and automatic signal rungs add `members`, `error`; rung 2 also adds `graceful` |
| `rung-skipped` | A rung is unsupported on this platform | `rung`, `name`, `error` |
| `rung-emptied` | A rung is found empty, at the first check or after its wait; once per stop | `rung` |
| `rung-timeout` | A rung's bound expires without proving the unit empty; `error` records any failure to list survivors | `rung`, `alive`, `error` |
| `heartbeat-failed` | A heartbeat write fails | `sequence`, `error` |
| `ended` | Outcome publication has been attempted, even if it failed; check the Run error and `outcome.json` separately for success | `outcome` |
| `advance` | A run of the launcher's advance of the task (`--advance`) has exited or failed to start, after the outcome was recorded; a failed advance logs two | `command`, `exit` when it ran, `error` when it failed |
| `settled` | Resume settlement publishes a new outcome after the launcher is gone and the unit is proven empty; returning an existing outcome does not emit it | `outcome` |

`rung-emptied` is written once for the rung that ends the stop: rung 1 can be found empty before the ladder's first check, during its wait or during cooperative supervision, and each of those paths returns after writing it.
`TestEventsDocumented` pins the launcher's typed event vocabulary to this table in both directions.

### Stop requests

`Stop`, the endpoint's `stop`, and SIGTERM or SIGHUP delivered to the launcher all request the one ladder; every request waits for it and answers with its end, and a stop after the end returns at once.
`Cancel`, the endpoint's `cancel`, is rung 1 alone: it writes the marker unless a valid result is present and does not escalate.
If the worker exits with the marker present while descendants remain, the launcher starts the bounded cooperative wait at rung 1.
After that bound expires it continues supervising without signalling descendants; only an explicit `Stop` request (including a stop signal) escalates past rung 1.
Automatic cleanup checks the marker and dispatches each signal under the attempt lock: a `Cancel` accepted before dispatch switches cleanup to cooperative supervision, while a signal dispatched before `Cancel` cannot be undone.
An interrupt delivered to the launcher, such as Ctrl+C in its terminal, is forwarded to the worker with `Unit.Interrupt` and does not stop the attempt.
`StopSignals` lists the signals the caller relays to `Run`.

### Limits

- `CTRL_BREAK` is not a dependable graceful rung: it reaches only a worker sharing the launcher's console, and a worker may treat it as fatal; the rung's event records `"graceful": false` on Windows, and the marker is the graceful stop on every platform.
- Escapes from a process-group unit are outside the unit, so the ladder neither waits for nor reaches them ([containment limits](#limits)).
- The Tier 2 lab runs, the Herdr tab mode running the launcher in its pane and the retirement of the Windows-only worker stop path remain; see the [resume decision](decisions/0048-artisan-resume.md).

## Heartbeat

The launcher writes `heartbeat.json` in the attempt directory once when `Run` starts the worker and then every `HeartbeatInterval` (`DefaultHeartbeatInterval`, 5 s), replacing it atomically:

```json
{
  "schema": "guildhall.attempt-heartbeat/1",
  "repository": "app",
  "task_id": "t-1",
  "step": "build",
  "attempt": 1,
  "sequence": 42,
  "time": "2026-10-21T10:00:05Z",
  "worker_state": "running",
  "last_output_at": "2026-10-21T09:58:12Z"
}
```

- It is `artisan.Heartbeat`, written with `WriteHeartbeat`; reconcile and the interpreter's [`worker-stalled`](interpreter.md#worker-stalled-observations) observation read it with `ReadHeartbeat`.
- It is an observation, not a store record: it has no receipt, replay or ownership meaning, and reading or writing it never changes a record.
- `sequence` is positive and increases with every write, continuing from a heartbeat an earlier launcher of the attempt left.
- `worker_state` is the launcher's phase: `starting`, `running` or `stopping`; it is nonblank UTF-8 of at most 256 bytes.
- `last_output_at` comes from `LauncherConfig.LastOutput`; without it, or while it reports no time, it is when `Run` began.
  `artisan start` reports the newest modification time of the worker's standard output and error when they are regular files, as `launch.Process` makes them by appending both to `launcher.log`, so a launcher's own lines there count as output too; a worker writing to a terminal is not observed, the launcher never reading it.
- `LauncherConfig.Beat` is called with each beat once it is written; a failed write calls nothing.
  `artisan start --stall-interval <duration>` uses it to send the Director the engine's [`worker-stalled`](interpreter.md#worker-stalled-observations) message, `interp.StalledMessage`, when a beat's `last_output_at` is older than the interval relative to its `time`: once per crossing, since the message id is derived from the attempt and `last_output_at`, and again after new output is followed by another crossing.
  The send goes through the store and then [wakes the Director](messaging.md#waking-the-director) as `guildhall`, off the supervision loop, so a slow store or pane never delays the next beat; a beat arriving while a send is in flight is checked after it, and a failed send is retried on the next beat.
  An advance that later observes the same crossing replays the message and sends nothing new.
  A later time than the beat is reported as the beat's time.
- The beat is written from the launcher's supervision loop, not a separate timer, so a launcher whose loop is blocked stops beating while its process and endpoint still live.
  Beats continue during result grace and cooperative supervision, but pause while the stop ladder waits on a signal rung.
- A failed write is logged as `heartbeat-failed` in `events.jsonl` and retried on the next beat; it never ends the attempt.
- `WriteHeartbeat` validates nonempty repository, task and step names, a positive attempt and sequence, nonzero times with `last_output_at` not after `time`, and the worker-state bound above; invalid values return `ErrInvalidHeartbeat` without writing.
  It sets the schema and replaces the file without reading the previous beat; the launcher is responsible for increasing the sequence.
- `ReadHeartbeat` decodes the file with [`strictjson`](contracts/strictjson.md) and never guesses: no file is `ErrNoHeartbeat`, a newer schema matches `strictjson.ErrNewerSchema`, and an unversioned, malformed or rule-breaking file is `ErrInvalidHeartbeat`.
  Other filesystem read failures are returned unchanged.

## Reconcile

`reconcile.Classify` classifies an owned attempt from what outlives its launcher: the attempt directory, the store's ownership record, and the live state of the identity, endpoint and containment the record names.
`reconcile` cannot read the engine's hand-over mark, so its caller passes it as `Attempt.Handed`: `launch.Classify`, behind `artisan status` and `director status`, sets it from `interp.HandedToDirector`, and a mark that cannot be read is no hand-over.
Its [doc comments](../internal/reconcile/reconcile.go) own the API contract.
It never signals, writes attempt state, takes over or relaunches anything; acquiring the lock may create `attempt.lock`.

The first rule that applies decides:

| Order | Evidence | State |
|---|---|---|
| 1 | `outcome.json` | its state: `finished`, `cancelled` or `failed`; `unknown` when it cannot be read, a newer schema reported apart from damage |
| 2 | Cancel marker | `cancelled` |
| 3 | Valid result | `finished` |
| 4 | No ownership record | `handed`, reason `handed to the Director`, when `Attempt.Handed` says the engine [handed the attempt to the Director](interpreter.md#director-stages) as a Director stage, which runs no launcher; otherwise `absent` |
| 5 | Launcher identity gone, and no containment recorded or `Contained` proves the unit empty; a recorded job or cgroup without its worker is checked by name | `exited` |
| 6 | Launcher identity gone, and contained processes survive or cannot be checked | `unknown`, with the survivors |
| 7 | Launcher identity live, heartbeat that `ReadHeartbeat` refuses | `unknown`, whatever the endpoint answers |
| 8 | Launcher identity live, heartbeat older than the stale threshold (`DefaultStaleAfter`, 30 s) | `stuck`, whatever the endpoint answers |
| 9 | Launcher identity live, endpoint answers `status` for this attempt and launcher | `running` |
| 10 | Anything else, such as a live launcher with a silent endpoint or an unverified identity | `unknown` |

`Classify` reads outcome, marker and result while holding the same exclusive `attempt.lock` as launcher decisions, using `artisan.LockAttempt`.
An in-flight cancel or stop must finish publishing its marker before classification can accept a result, even when the worker publishes between the launcher's result check and marker write.
Lock acquisition is bounded by the caller's context and `LockTimeout` (2 s); failure yields `unknown` with the lock error.
An ownership record whose attempt directory does not exist has no files to read: it is classified by the owner checks alone, rules 5 to 10, and `Reason` ends with `no attempt directory`; classifying creates nothing.
Acquiring the lock may create its file but writes no attempt state.
The lock is released before owner and endpoint checks.

The classification carries its evidence: the launcher's liveness, what the endpoint answered, the last heartbeat and its age, and any survivors.
A heartbeat is evidence only: `stuck` is never exit and grants no relaunch, take-over or sweep, and a crashed launcher's frozen heartbeat is reported while the identity rule classifies it `exited`.
A heartbeat that names another attempt is ignored.
When classification reaches the owner checks, a heartbeat file that `ReadHeartbeat` refuses is reported in `Reason`, even if the launcher is gone or unverified; earlier file decisions and an absent owner do not read the heartbeat.
A missing file (`ErrNoHeartbeat`) remains no heartbeat; see the [API contract](../internal/reconcile/reconcile.go) for refusal handling.
The endpoint call is bounded by the caller's context and `EndpointTimeout` (2 s).

## Commands

`guildhall artisan start`, `stop`, `status` and `resume` name an owned attempt with `--store <file> --state <dir> --repo <id> --task <id> --step <id> --attempt <n>`.
The repository flag is `--repo`, as on `task start`, `step done` and `decide` ([D53](decisions/0053-cli-role-grammar.md)); the pre-release `--repository` is a usage error, exit code 2, naming `--repo`.
A [copy](running.md#copies), `--copy <dir>` or `GUILDHALL_COPY`, stands in for `--store` and `--state`; it does not apply to an attempt in a Herdr tab.
The attempt directory is `<state>/<repository>/<task>/<step>-<n>`, as the interpreter prepares it; the result file is `result.json` in it and names the task and `<step>-<n>`.
`artisan.AttemptKey` owns that layout (`Dir`, `AttemptID`, `ResultFile`, `ModelDir`) for both.
`model` in the attempt directory is the only part a model-run Artisan may write, holding the copy of its prompt and its draft result, which its wrapper publishes as `result.json`; the result, its cancel marker, `outcome.json`, the lock, the events file and `launcher.log` are never in it.
They are the composition package `internal/launch`, whose [doc comments](../internal/launch/launch.go) own the API contract.

With those identity flags, `artisan start --adapter fake|claude --prompt <file> --workdir <dir>` (the names [`runtimes`](adapters.md#running-an-attempt) holds) runs an attempt the store already records as running; it does not create or begin a store attempt.

Before taking ownership, it validates the command and endpoint path, creates the attempt directory and containment unit, and constructs the launcher.
It then acquires the attempt lock, waiting at most five seconds or until the caller's context ends, and holds it across the `outcome.json` check and ownership acquisition.
An existing outcome is refused with `artisan.ErrDecided`; publication uses the same lock, so it cannot race between that check and takeover.
The lock is released before endpoint binding and worker startup.
These setup failures leave ownership unchanged and start no worker, so correcting the setup permits a retry.
Refusing an already-decided attempt preserves its ownership record and containment evidence; [`artisan resume`](#resume) finishes that attempt from `outcome.json` once its launcher is gone, not `run`.

1. it takes the attempt's ownership, recording a job or cgroup unit's description with it; a live owner refuses it with `ErrOwnerLive`, and an owner that cannot be proven exited, or whose recorded unit still has members, with `ErrOwnerUnknown`, before anything starts;
2. it binds the control endpoint and records its path; failure at either step records the attempt as failed with `FinishAttempt`, releasing ownership without starting the worker, and returns the setup error joined with any finalization error.
   A finalized failed attempt requires a new attempt for subsequent work;
3. it starts the worker in the attempt's containment unit and records the containment in `containment.json` and, with the worker identity, in the ownership record, with state `running`;
4. it supervises the attempt with the stop ladder and heartbeat until it is decided and the unit is empty; a positive `--timeout` requests the stop ladder when it expires (zero, the default, imposes no limit), as do the endpoint's `stop` and SIGTERM or SIGHUP;
5. it records the store outcome with `FinishAttempt`, which releases the ownership;
6. with `--advance`, it then advances the task over the copy as its [watcher](running.md#the-watcher): `step done` after a finished attempt with a valid result, otherwise `task start`, with the output appended to `launcher.log` and an `advance` event in `events.jsonl` for each run; a failed advance is run once more and, failing again, [told to the Director](running.md#the-watcher) as `advance-failed`, and never changes the outcome.
   `--advance` needs a copy, `--copy` or `GUILDHALL_COPY`, and is a usage error without one; `launch.Process` passes `--copy <dir> --advance` only when the engine runs from a copy.

With a positive `--stall-interval` (zero, the default, observes nothing), the launcher also sends the Director `worker-stalled` from its heartbeat when the worker has been silent past the interval ([heartbeat](#heartbeat)); `launch.Process` passes the engine's `stall_interval` when it is positive.

`--profile <name>` names the profile in the Director `--copy`; see [Artisan permissions](adapters.md#artisan-permissions) for grant resolution and runtime support.
`--model <model>` and `--effort <effort>` are the profile's [runtime settings](adapters.md#runtime-settings), which the runtime runs the attempt on; `launch.Process` passes them when the profile sets them, and a setting the runtime cannot honour is exit 1 before the attempt is owned.
`--profile`, `--model` and `--effort` apply only to an owned attempt; in a Herdr tab they are a usage error.
Neither `artisan start` nor `claude-artisan` accepts `--allow-tool`.

The worker's output goes to standard error, and standard output carries one JSON line: the attempt, the outcome, the store outcome and any accepted result.
Survivors after the last rung leave the attempt running and owned, and the endpoint reports a stop error.
`outcome.json` records the survivors; `status` may still report a terminal decision from a marker or result, which does not prove termination.

| Launcher outcome | Result | Store outcome | Exit status |
|---|---|---|---|
| `finished` | `succeeded` | `succeeded` | 0 |
| `finished` | `failed` | `failed` | 4 |
| `failed` | none | `failed` | 4 |
| `cancelled` | any, rejected as late | `stopped` | 5 |

Any error, including a refused ownership, worker startup failure, containment-recording failure and survivors, exits 1; a usage error exits 2.
Startup failures remain returned errors even when the failed outcome is successfully written and recorded in the store.

`artisan stop` asks the owner to stop through the endpoint its unreleased ownership record names and waits for the ladder's end within `--timeout` (one minute).
It never signals a process itself.
Without a successful endpoint answer, success requires no ownership record, or a launcher proven gone and its recorded unit proven empty; missing containment is unverified, including during startup.
A released record alone does not prove termination while its launcher still lives.
On a released record the report's `launcher` names the recorded launcher: `advancing` while it lives, running the task's [advance](running.md#the-watcher) after releasing its attempt, which exits 1 as not proven stopped; `gone` once it has exited, when stop exits 0 if the recorded unit is proven empty; `unverified` when it cannot be probed.
It prints what the endpoint did and the classification afterwards.
Endpoint request errors, including reported surviving processes, are preserved and exit 1; a transport failure can still succeed if the fallback proves termination.
Unproven termination exits 1 regardless of a `finished`, `cancelled` or `failed` classification.
`artisan status` prints the [classification](#reconcile) and exits 0; an attempt with no directory and no ownership record is `absent`.

### Resume

`artisan resume`, with the same attempt flags, reconciles an attempt whose launcher has exited, such as after its terminal or the Herdr server restarted; it never starts a worker.
It is `launch.Resume`, whose [doc comments](../internal/launch/resume.go) own the API contract.

1. With no ownership record, or a released one, there is nothing to resume.
2. A live launcher identity is refused (`owner: live`), and one that cannot be proven gone, or a record without containment, is refused as unverified; nothing is signalled or written.
   A job or cgroup is recorded with the ownership, so a record without containment is a process group's owner gone before its worker was recorded ([limits](#limits)).
3. With the launcher proven gone, it sweeps the recorded unit with `artisan.Sweep` until the owner-gone rule proves it empty, within ten seconds (`SweepTimeout`); a unit that does not empty is an error and the attempt stays owned.
4. Under the attempt lock, while the record still names that launcher, `artisan.Settle` decides the attempt with the launcher's precedence and keeps an existing `outcome.json`, and `FinishAttempt` records the store outcome and releases the ownership.
   An owner still in `starting` that the files do not decide is `failed: launcher exited before its worker was recorded`; any other is `failed: launcher exited without a decision`.

A second resume finds the record released; a resume interrupted after `outcome.json` was written records the same outcome when run again, because its store write's mutation id comes from the attempt and the gone launcher's identity.
It prints one JSON line, the owner it found (`none`, `released`, `live`, `unverified` or `exited`), the processes it swept, the outcome, the store outcome and the classification afterwards, and exits 0 when nothing was left to resume or the attempt was settled, otherwise 1.

The owner-gone rule is `reconcile.ProbeOwner`: the recorded launcher identity is probed, the record's containment translated and `artisan.Contained` asked for survivors; an owner is gone only when its launcher is gone and its recorded unit is proven empty.
Take-over (`launch.ProbeOwner`), classification and the sweep all read it.

`artisan.Sweep(containment, worker)` kills what is still in a recorded unit from outside the launcher that created it: a job is opened by name and terminated, a cgroup killed through `cgroup.kill` and removed once empty, and each member of a process group, found by its leader as `Contained` finds it, killed with `artisan.Kill`.
Sweep does not check ownership, and only `Contained` returning nothing afterwards proves the unit empty.
`artisan.Kill(identity)` ends a process only while a live process still has its PID and token, through a pidfd on Linux and a process handle on Windows; macOS checks and then signals, so a PID reused between the two is a limit there.
Escapes from a process group, such as a `setsid` grandchild, are not members and are not swept.

`launch.ResumeArgv` is the command a pane registers to run after a restart: `guildhall artisan resume` with the attempt's flags, starting with the plain command name Herdr requires.
`herdr.Client.ReportAgent` registers it with `herdr pane report-agent --source guildhall`, refusing a resume command that starts with a path or an option (`herdr.ErrResumeArgv`).
Production registration is deferred to L7 part 3, which wires the `herdr.Client.ReportAgent` call with `launch.ResumeArgv` when running the launcher in the Herdr tab.

### Harness

`guildhall artisan harness inventory|kill-owned|probe --lab` is the lab's test tooling and refuses to run without `--lab`.

| Command | Does |
|---|---|
| `inventory` | With the attempt flags, `--inventory <dir>` or both, lists the launcher and worker the ownership record names and every process recorded in the inventory directory, each with its role, identity and liveness |
| `kill-owned` | Kills each process `inventory` lists that is still alive, with `artisan.Kill`, by identity and never by name, and lists those it killed |
| `probe` | Reads a JSON array of identities from standard input and prints each with its liveness |

The inventory directory holds one `artisan.ProcessRecord`, an identity with a role, per process, written as `<role>-<pid>.json` by `artisan.RecordProcess`, atomically, and read by `artisan.ReadProcessRecords`, which refuses a record without a positive PID, a token or a role, or one not stored under its own name, with `ErrInvalidProcessRecord`, and a missing directory with an error matching `fs.ErrNotExist`.
The fake Artisan's `inventory` directive ([fake directives](adapters.md#fake-adapter)), the `artisantest` worker and this harness write and read the directory only through them and spell neither the file name nor the fields.
The `artisantest` worker's `Entry` is a `ProcessRecord`, written by `RecordProcess` and read with `ReadProcessRecords`.
Test metadata lives in roles: `grandchild-setsid` identifies a grandchild started in its own session; `breakaway-refused` and `breakaway-succeeded` record the breakaway outcome under the worker's identity.

### Custody

The launcher opens the store for each record operation and closes it before the next, and never holds it while the worker runs.
Ownership is durable state in the store, not a held handle, so other commands read, write and refuse competing runs while an attempt runs.

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
The endpoint tests bind real sockets in a short temporary state directory on every runner; their inventory is in the [package contract](contracts/artisan.md#tests).

`artisantest.RunStopContract` is the stop ladder and precedence contract.
It runs against this platform's backend in `launcher_test.go` on the Linux, Windows and macOS runners, and against `fakeUnit`, with the test binary playing a worker that can honour the marker, publish its result after a delay or ignore interrupts, and a file double for the result and marker.
Its cases are the P0 lifecycle scenarios: a cooperative worker stopping at rung 1 (L6); an ignored interrupt escalating, with every timed-out rung naming the processes alive (L7); a stop or cancel before the worker starts, and a stop during startup leaving no owned process (L15); a result before the stop finishing with no marker, a result after the marker rejected as late, and a result racing the stop decided exactly once (L16); a result after cancel rejected while the worker exits (L17); concurrent and repeated stops sharing one ladder, with one interrupt and at most one kill (L18).
It also covers SIGTERM and SIGHUP stopping the attempt, a forwarded interrupt leaving it running, an invalid result failing it, a decided attempt not run again, and survivors reported after the last rung.
On Windows it also holds a published result without sharing while the launcher decides, as a rename in progress does, and requires the attempt finished; every `AttemptFiles` adapter, the double and `launch.Files`, must pass it.

`internal/reconcile/reconcile_test.go` runs `Classify` against recorded attempt directories and live processes: the test binary plays the worker tree, an in-process launcher with a bound endpoint, and a separate launcher process for the crash.
The four heartbeat cases are a wedged launcher whose supervision loop is really blocked (stuck while its identity and endpoint live, then running again); a stalled worker whose fresh heartbeat with an old `last_output_at` is running, not stuck; a stale heartbeat while the launcher lives (stuck, take-over refused with `ErrOwnerLive`, running once beats resume); and a killed launcher process whose frozen heartbeat is reported while the identity decides, `unknown` while contained processes survive and `exited` once they are gone.
The pinned scratch cases are the files deciding first (L22), a gone launcher with an emptied or surviving unit (L13, L14), a broken endpoint of a live launcher (L19), a reused PID (L20) and a leftover endpoint file (L21).
A newer-schema and a malformed heartbeat beside a live launcher whose endpoint answers are `unknown`, with the reason naming the schema or the defect.
Every case checks that the processes it caused are gone.

The command tests are listed in the [launch contract](contracts/launch.md#tests): `RunStopContract` with `launch.Files` and with the fake Artisan as the worker, the outcome translation table, owned runs against a real store, resume against real units, and the custody acceptance with each command in its own process.
