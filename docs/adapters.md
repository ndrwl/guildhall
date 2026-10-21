# Artisan adapters

This page describes the baseline contract between Guildhall and an Artisan runtime (R1.23), the result file that completes an attempt, the test-only fake adapter (R1.22), and how Guildhall reaches Herdr to show an Artisan in a tab (R1.38).
It describes what is built today; no runtime or platform is claimed as supported until its acceptance passes (R1.27).

## Baseline contract

Guildhall gives an Artisan one task attempt as a spec:

| Field | Meaning |
|---|---|
| Task id and attempt id | The attempt's immutable identity: 1 to 128 letters, digits, `.`, `_` or `-`, starting with a letter or digit |
| Working directory | Absolute path the Artisan runs in |
| Prompt file | Absolute path of the prompt the Artisan reads |
| Result file | Absolute path the Artisan writes its result to; distinct from the prompt file |
| Environment | Extra `NAME=VALUE` entries |

An adapter turns the spec into a program invocation: an argument list, never shell text.
Guildhall starts it through the [owned attempt launcher](artisan.md#commands) or in a [Herdr tab](#running-an-attempt).
The attempt is complete only when the result file holds a valid result for the attempt's exact identity.
Process exit, a closed or vanished tab, a Herdr restart and Herdr's agent-state signals never complete an attempt (R1.23, R1.52).
Process supervision and stop semantics depend on the launch mode; see the linked command documentation above.

Each adapter declares optional capabilities: a pre-assigned session id, native background sessions, hooks, structured output and remote control.
They all default to absent, and the core uses none of them until a capability is proven (R1.24).

## Result file

The result file is JSON, one object, version 1:

```json
{
  "schema": "guildhall.artisan-result",
  "version": 1,
  "task_id": "task-1",
  "attempt_id": "attempt-1",
  "status": "succeeded",
  "summary": "What was done, in one or more sentences.",
  "outputs": [{"path": "docs/notes.md", "description": "optional"}],
  "landed_ref": "refs/heads/task-1"
}
```

| Field | Rule |
|---|---|
| `schema` | Exactly `guildhall.artisan-result` |
| `version` | A positive integer; a version newer than this Guildhall supports is refused, never guessed (R1.11) |
| `task_id`, `attempt_id` | Must equal the attempt's identity; a result for any other attempt is rejected |
| `status` | `succeeded` or `failed`; both complete the attempt |
| `summary` | Not blank |
| `outputs` | Required, possibly empty; each `path` is a clean relative path with `/` separators inside the working directory |
| `landed_ref` | Optional; no whitespace or control characters |

Guildhall rejects a result file that is larger than 1 MiB, is UTF-16 or not exactly one JSON object, repeats a key, has an unknown or missing field, or breaks a rule above.
All field names, including nested output fields, must use the exact lowercase spelling shown above; alternate casing and case-folded duplicates are rejected.
A rejected result is reported as rejected; it is never treated as success or failure of the work.

Guildhall writes a result once: it writes a temporary file beside the result path and hard-links it into place, so a reader sees either no file or the whole result, and an existing result is never replaced.
The result directory must be on a file system that supports hard links.
The [Herdr tab mode](#running-an-attempt) refuses to launch when the result file already exists; the [owned mode](artisan.md#commands) uses the launcher's decision precedence.
An identity-bound stop first persists `<result>.cancelled`, containing the attempt's `task_id` and `attempt_id`.
Result reads and writes reject that cancelled attempt, including after a client restart; a mismatched or invalid cancellation sidecar is also rejected.
The marker remains even if termination fails, but that failure is explicitly reported rather than claiming the worker stopped.
Do not remove the marker to retry: use a new attempt and result path.
This is attempt-local cancellation evidence, not task completion or a separate task store.

## Fake adapter

The fake adapter is a test-only runtime with no optional capabilities.
It runs `guildhall fake-artisan --task <id> --attempt <id> --prompt <file> --result <file>`, which reads the prompt, prints its progress, and writes a result whose summary records the prompt's size and SHA-256 digest.

Prompt lines of the form `guildhall-fake: <key>=<value>` choose its behaviour; each key may appear once, and an unknown key or value is refused before anything is written.

| Directive | Effect |
|---|---|
| `outcome=succeeded` or `outcome=failed` | The result status (default `succeeded`) |
| `delay=<duration>` | Simulated work before the result, from `0s` to `10m`; an interrupt ends it without a result |
| `fault=no-result` | Exit without writing a result |
| `fault=malformed` | Write a truncated result file |
| `fault=wrong-identity` | Write a result for another attempt id |
| `fault=exit-error` | Write a valid result, then exit with status 1 |
| `result=delay`, `result=after-cancel` or `result=never` | When the result is written: after the delay (default), only once the cancel marker is present, as a late result published beside it, or never |
| `lifetime=<duration>` | Every process of the fake's tree exits by then, without a result if none was written; above `0s` and up to `1h`, default `15m` |
| `grandchildren=<n>` | The worker starts `n` processes, from 0 (default) to 8 |
| `grandchild-cmd=fake` or `grandchild-cmd=native` | Each grandchild is the fake in role `grandchild` (default), which honours the marker and interrupts as the worker does, or a native command that knows nothing of the marker: `sleep` on Unix, `ping` on Windows, for the lifetime |
| `ignore-cancel=true` | No fake process exits on the cancel marker; the adapter still refuses a result written after it |
| `ignore-interrupt=true` | Every fake process handles and discards an interrupt (SIGINT, CTRL_C, CTRL_BREAK) |
| `interactive=true` | The worker echoes each line of its standard input |
| `inventory=<absolute dir>` | Every fake process records its identity there as `<role>-<pid>.json`, and the worker records each native grandchild |

Unless `ignore-cancel` is set, the cancel marker (the result file's `.cancelled` sidecar) ends a fake process's work without a result, except that `result=after-cancel` makes the worker publish a late result even with `ignore-cancel=true`.

## Running an attempt

Without Herdr flags, `guildhall artisan run` owns a stored attempt as its launcher; see [Artisan commands](artisan.md#commands).

`guildhall artisan run --adapter fake --task <id> --attempt <id> --prompt <file> --result <file> --workdir <dir> --herdr-session <name> --herdr-workspace <id>` launches one attempt in a new tab of that workspace and waits for its result file.

- The tab is labelled `artisan <task>/<attempt>` unless `--tab-label` is given.
  `--herdr-exec` names the Herdr program, repeated once per word when the program needs leading arguments; the default is `herdr` on `PATH`.
  On timeout or interruption without a result the Artisan is left running unless `--stop-on-timeout` is given; a rejected result does not trigger stopping, and Guildhall never closes the tab.
  On native Windows that option establishes ownership before work starts, then terminates and waits on the exact retained process handle when the wait ends.
  Elsewhere it requests Ctrl+C and explicitly reports that termination is unverified.
- `--timeout` bounds launch setup and the result wait together (default 30 minutes).
  A stop request after that deadline has a separate 30-second budget in the Herdr path.

A valid result is printed to standard output as one JSON line.

| Exit status | Meaning |
|---|---|
| 0 | A valid result with status `succeeded` |
| 1 | An error or a rejected result |
| 2 | A usage error |
| 3 | No result yet when the wait ended; the attempt is not finished |
| 4 | A valid result with status `failed` |

### Native Windows stop boundary

The cooperating fake worker connects to a one-use loopback listener with a random attempt token, task/attempt identity, result path, PID and native creation time, and waits for acknowledgement before doing work.
The owner verifies those values and the process image against its executable, retains a Windows handle with only terminate/query/synchronize rights, then acknowledges startup.
Stopping uses `TerminateProcess` and waits for that same handle to signal exit: it never reopens a PID, kills by name, targets the shell, closes the tab, adjusts privileges, or deletes unfinished work.
This stops the exact fake worker only, not arbitrary runtimes or descendant process trees; no general supervisor or service is introduced.
The token prevents accidental adoption, not malicious interference by another process with the same user's access.
Missing ownership, image/creation mismatches and failed termination are explicit failures; there is no Windows Ctrl+C fallback claiming success.
The native handle capability is explicitly unsupported outside Windows, where the existing interrupt-only path remains.

## Herdr access

Guildhall drives Herdr through its documented command-line API, which prints JSON, rather than speaking the socket protocol itself.

- Every call names its session with `--session <name>` and sets `HERDR_SESSION` to the same name; the inherited `HERDR_SESSION`, `HERDR_SOCKET_PATH`, `HERDR_ENV` and pane, tab and workspace id variables are removed, so only the explicit session selects the server.
- The session name is required and Herdr's `default` session is refused.
  Select a dedicated session provisioned outside Guildhall; a non-default name alone does not establish ownership of the session.
- Before launching, `herdr status --json` must report a running server for that session, and both the client and the server must be Herdr 0.9.5 or newer.
  Guildhall never starts, stops, restarts or deletes a Herdr server or session.
- Guildhall uses `status`, `workspace list|create`, `tab create|list|get|close` and `pane process-info|run|send-keys`, always with `--no-focus` when creating.
- The client also supports `pane report-agent`; see [resume](artisan.md#resume) for its registration contract and production wiring status.
- Herdr's tab creation takes no command, so Guildhall waits until the new tab's shell is the only foreground process, then types the command quoted for that shell with `pane run`.
  POSIX shells (`sh`, `bash`, `dash`, `ksh`, `zsh`) and PowerShell are supported; any other shell is refused.
  For a POSIX shell, a program named by a Windows drive path is written with `/` separators, because `dash` under Git for Windows cannot start it with `\`.
  Arguments PowerShell would alter are refused for PowerShell: empty arguments, arguments containing `"` or typographic single quotes, and arguments containing whitespace that end in `\`, which Windows PowerShell 5.1 merges with the next argument.
- A tab Guildhall creates is the only kind it can type into, interrupt or close, and the close rule of R1.54 applies; `artisan run` never closes a tab.

## Verification status

| Check | Where | Status |
|---|---|---|
| Contract, result validation, fake adapter, Herdr argument lists and response parsing | CI unit tests on Linux, Windows and macOS, against Herdr responses recorded from Herdr 0.9.5 | Run on every change |
| Fake Artisan in a Herdr tab: result recorded, failed and malformed results, closed tab not counted as completion, existing result refused | Linux (WSL, zsh panes) in an isolated named Herdr 0.9.5 session | Passed |
| The same in native Windows panes running Git Bash and Windows PowerShell 5.1 | Native Windows in an isolated named Herdr 0.9.5 session | Passed |
| Stop by Ctrl+C ends the fake Artisan | Linux zsh and Windows Git Bash panes | Passed |
| Stop by Ctrl+C ends the fake Artisan | Windows PowerShell 5.1 panes | Failed: the process kept running after Ctrl+C; Ctrl+C stopping remains unsupported in these panes |
| Identity-bound native handle stop; no later success, unrelated worker survival and unfinished-work preservation | Native Windows PowerShell 5.1 in an isolated named Herdr session, using the current locally built artifact with matching local/remote SHA-256 | Passed |
| Cancellation rejection, failed-stop semantics, handshake identity rejection and exact-process termination | Focused portable contract tests plus the native Windows test executable | Passed |
| Rendered command lines pass every argument unchanged when executed | `sh`, `bash`, `dash` and `zsh` on Linux; `sh`, `bash`, `dash` and Windows PowerShell 5.1 on native Windows; missing shells follow the [test environment contract](contracts/testenv.md) | Passed |
| Native handle stop contract: verified termination, repeated and concurrent stops, natural exit, ended context, idempotent close, no use of a released handle | Native Windows test executable against a live helper process | Passed |
| macOS in Herdr | Not run | macOS has CI only, not live acceptance (R1.30) |

Recording pane bindings, reconciling after a restart and the per-task watcher are not built yet.
