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
Guildhall starts it either as a direct child process, which tests and CI use, or in a new tab of a named Herdr session.
The attempt is complete only when the result file holds a valid result for the attempt's exact identity.
Process exit, a closed or vanished tab, a Herdr restart and Herdr's agent-state signals never complete an attempt (R1.23, R1.52).
A direct child process that exits with no result file is reported as exited without a result, which is not a completion.
Guildhall can stop an Artisan: it kills a direct child process, and it sends Ctrl+C to the pane of a tab it created.

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

Guildhall rejects a result file that is larger than 1 MiB, is not exactly one JSON object, repeats a key, has an unknown or missing field, or breaks a rule above.
A rejected result is reported as rejected; it is never treated as success or failure of the work.

Guildhall writes a result once: it writes a temporary file beside the result path and hard-links it into place, so a reader sees either no file or the whole result, and an existing result is never replaced.
`guildhall artisan run` refuses to launch when the result file already exists; a new attempt needs a new result path.

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

## Running an attempt

`guildhall artisan run --adapter fake --task <id> --attempt <id> --prompt <file> --result <file> --workdir <dir>` launches one attempt and waits for its result file.

- Without Herdr flags the Artisan runs as a direct child process whose output goes to standard error; a child still running when the wait ends is stopped.
- With `--herdr-session <name> --herdr-workspace <id>` the Artisan runs in a new tab of that workspace, labelled `artisan <task>/<attempt>` unless `--tab-label` is given.
  `--herdr-exec` names the Herdr program, repeated once per word when the program needs leading arguments; the default is `herdr` on `PATH`.
  When the wait ends without a result the Artisan is left running unless `--stop-on-timeout` asks for Ctrl+C, and Guildhall never closes the tab.
- `--timeout` bounds the wait (default 30 minutes).

A valid result is printed to standard output as one JSON line.

| Exit status | Meaning |
|---|---|
| 0 | A valid result with status `succeeded` |
| 1 | An error, a rejected result, or a direct child that exited without a result |
| 2 | A usage error |
| 3 | No result yet when the wait ended; the attempt is not finished |
| 4 | A valid result with status `failed` |

## Herdr access

Guildhall drives Herdr through its documented command-line API, which prints JSON, rather than speaking the socket protocol itself.

- Every call names its session with `--session <name>` and sets `HERDR_SESSION` to the same name; the inherited `HERDR_SESSION`, `HERDR_SOCKET_PATH`, `HERDR_ENV` and pane, tab and workspace id variables are removed, so only the explicit session selects the server.
- The session name is required and Herdr's `default` session is refused.
- Before launching, `herdr status --json` must report a running server for that session, and both the client and the server must be Herdr 0.9.5 or newer.
  Guildhall never starts, stops, restarts or deletes a Herdr server or session.
- Guildhall uses `status`, `workspace list|create`, `tab create|list|get|close` and `pane process-info|run|send-keys`, always with `--no-focus` when creating.
- Herdr's tab creation takes no command, so Guildhall waits until the new tab's shell is the only foreground process, then types the command quoted for that shell with `pane run`.
  POSIX shells (`sh`, `bash`, `dash`, `ksh`, `zsh`) and PowerShell are supported; any other shell is refused, and arguments PowerShell would alter (empty, containing `"`, or containing typographic single quotes) are refused for PowerShell.
- A tab Guildhall creates is the only kind it can type into, interrupt or close, and the close rule of R1.54 applies; `artisan run` never closes a tab.

## Verification status

| Check | Where | Status |
|---|---|---|
| Contract, result validation, fake adapter, Herdr argument lists and response parsing | CI unit tests on Linux, Windows and macOS, against Herdr responses recorded from Herdr 0.9.5 | Run on every change |
| Fake Artisan in a Herdr tab: result recorded, failed and malformed results, closed tab not counted as completion, existing result refused | Linux (WSL, zsh panes) in an isolated named Herdr 0.9.5 session | Passed |
| The same in native Windows panes running Git Bash and Windows PowerShell 5.1 | Native Windows in an isolated named Herdr 0.9.5 session | Passed |
| Stop by Ctrl+C ends the fake Artisan | Linux zsh and Windows Git Bash panes | Passed |
| Stop by Ctrl+C ends the fake Artisan | Windows PowerShell 5.1 panes | Failed: the process kept running after Ctrl+C; stopping in PowerShell panes is not available |
| macOS in Herdr | Not run | macOS has CI only, not live acceptance (R1.30) |

Recording pane bindings, reconciling after a restart and the per-task watcher are not built yet.
