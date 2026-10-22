# Artisan adapters

This page describes the baseline contract between Guildhall and an Artisan runtime (R1.23), the result file that completes an attempt, the test-only fake adapter and the minimal Claude adapter (R1.22), and how Guildhall reaches Herdr to show an Artisan in a tab (R1.38).
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
| `inventory=<absolute dir>` | Every fake process records its identity there with `artisan.RecordProcess` ([inventory](artisan.md#harness)), and the worker records each native grandchild |
| `output=<path>:<text>` | Before the result, write `<text>` and a newline to `<path>`, a relative slash-separated path in the working directory, and list it as the result's one output, as a stage's output port needs |

Unless `ignore-cancel` is set, the cancel marker (the result file's `.cancelled` sidecar) ends a fake process's work without a result, except that `result=after-cancel` makes the worker publish a late result even with `ignore-cancel=true`.

## Claude adapter

The Claude adapter runs one attempt as a non-interactive Claude Code session, with no optional capabilities.
Its command is `guildhall claude-artisan --task <id> --attempt <id> --prompt <file> --result <file> [--claude <program>]`, a wrapper in the attempt's working directory that runs:

```text
claude --print <instruction> --permission-mode acceptEdits --add-dir <model directory>
```

- `claude` is looked up on `PATH` when the attempt starts unless `--claude` names an absolute program; sign-in is the machine's own (R1.28).
- The model directory is `model` beside the result file, the attempt directory's [model directory](artisan.md#commands); the wrapper replaces it on every run with a copy of the prompt file, `prompt.md`, and of the `inputs` and `skills` directories beside the prompt, so the attempt's result, cancel marker, outcome, lock, events and launcher log are never in a directory Claude may write.
- The instruction names the prompt copy and a draft result file, `model/result.json`, and gives the exact result object for the attempt's identity; Claude reads the prompt and writes the draft once, as its last action.
- `acceptEdits` lets Claude edit files in the working directory and the model directory; every other tool that needs permission, such as shell commands, is denied, because a print-mode session has no one to ask.
- Claude gets no standard input; its output goes to the wrapper's.

The wrapper keeps the result protocol, not the model:

- it refuses to start, starting nothing, when the result file exists or the attempt is cancelled, and a prompt inside the model directory is refused, since replacing the directory removes any stale draft;
- once Claude exits it publishes the draft's bytes at the result file by a hard link, never replacing a result; it does not validate them, so a malformed or misidentified draft is rejected by the reader like any other result, and no draft means no result;
- the cancel marker, an interrupt or the end of its context asks Claude to stop (an interrupt on Unix, termination on Windows), kills it after 10 seconds, and publishes nothing; so the owned launcher's stop ends at rung 1;
- Claude's exit status does not decide completion: a failing exit after a published draft is still reported.

This is less than R1.23 asks for: the session is Claude Code's print mode, not its interactive session in a Herdr pane, it runs only as an [owned attempt](artisan.md#commands) (`artisan start --adapter claude` refuses `--herdr-session`), and it passes no profile model or effort.
Transcripts, cost, session resume and the Herdr tab mode are later work.

## Conformance

Every runtime adapter passes one shared suite, `adaptertest.RunConformance`, before Guildhall launches it: it declares exactly its capabilities, refuses an invalid spec without a command, runs in the spec's working directory and environment, completes only from a valid result file for its identity, never completes without a result, on a malformed result or on a result for another attempt, leaves no result when stopped, and never replaces an existing result.
The suite makes no model calls; a runtime proves each behaviour through prompts it understands.

The fake runs it as a direct child process, inside the owned Artisan launcher, in a tab through the Herdr tab launcher, and on attempts the interpreter renders, mapped onto the adapter spec by `interp.Spec.AdapterSpec`, the mapping `launch.Process` starts attempts from.
The Claude adapter runs it as a direct child process, inside the owned Artisan launcher, and on interpreter-rendered attempts, over `claudetest`'s stub `claude` on `PATH`: the test binary under that name, which checks the arguments, reads the files the instruction names and plays the behaviour a `claude-stub:` prompt line asks for, with no model call.
Inside a test binary the interpreter's adapter guard admits `claude` only while `claude` on `PATH` is the stub `claudetest.Install` names in `GUILDHALL_TEST_CLAUDE_STUB`, so `task start` runs a profile naming `claude` over the stub and never a real Claude Code.
`TestLiveClaude` runs one real session instead when `GUILDHALL_CLAUDE_LIVE=1` is set; CI never sets it:

```sh
GUILDHALL_CLAUDE_LIVE=1 go test ./internal/adapter/claude -run TestLiveClaude -v -timeout 15m
```
CI has no Herdr server, so the tab run uses `adaptertest.ShellPanes`, which keeps the Herdr tab contract over a real POSIX shell; it runs on Linux and macOS, and Windows panes are covered by the live checks below.

## Running an attempt

Without Herdr flags, `guildhall artisan start` owns a stored attempt as its launcher with the fake or the [Claude adapter](#claude-adapter); see [Artisan commands](artisan.md#commands).

Package `internal/adapter/runtimes` is the one owner of the runtimes Guildhall can launch: `runtimes.Lookup` returns the runtime by the name `artisan start --adapter` and a profile's `adapter` use, `fake` or `claude`, and `runtimes.Names` lists them.
`artisan start` refuses any other name with a usage error that names it and lists them, in both modes.
`launch.Process` passes the engine's adapter name through unchanged, so an unknown name is refused by the `artisan start` process it starts and reported as that process exiting before it owns the attempt, with its log, as every other startup failure is.
The names are spelled once, as `adapter.FakeName` and `adapter.ClaudeName` in the leaf package every party imports; the lookup is keyed by them and `interp.FakeAdapter` and `interp.ClaudeAdapter` refer to them.
A new runtime is added to that package and nowhere else; the interpreter's test-binary guard, `interp.CheckAdapter`, stays in the interpreter.

`guildhall artisan start --adapter fake --task <id> --attempt <id> --prompt <file> --result <file> --workdir <dir> --herdr-session <name> --herdr-workspace <id>` launches one attempt in a new tab of that workspace and waits for its result file.

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
- A tab Guildhall creates is the only kind it can type into, interrupt or close, and the close rule of R1.54 applies; `artisan start` never closes a tab.

## Verification status

| Check | Where | Status |
|---|---|---|
| Contract, result validation, fake adapter, Herdr argument lists and response parsing | CI unit tests on Linux, Windows and macOS, against Herdr responses recorded from Herdr 0.9.5 | Run on every change |
| [Conformance](#conformance) of the fake as a direct child, inside the Artisan launcher and on interpreter-rendered attempts | CI on Linux, Windows and macOS | Run on every change |
| Conformance of the fake typed into a tab by the Herdr tab launcher and stopped by Ctrl+C, over `adaptertest.ShellPanes` | CI on Linux and macOS | Run on every change |
| Fake Artisan in a Herdr tab: result recorded, failed and malformed results, closed tab not counted as completion, existing result refused | Linux (WSL, zsh panes) in an isolated named Herdr 0.9.5 session | Passed |
| The same in native Windows panes running Git Bash and Windows PowerShell 5.1 | Native Windows in an isolated named Herdr 0.9.5 session | Passed |
| Stop by Ctrl+C ends the fake Artisan | Linux zsh and Windows Git Bash panes | Passed |
| Stop by Ctrl+C ends the fake Artisan | Windows PowerShell 5.1 panes | Failed: the process kept running after Ctrl+C; Ctrl+C stopping remains unsupported in these panes |
| Identity-bound native handle stop; no later success, unrelated worker survival and unfinished-work preservation | Native Windows PowerShell 5.1 in an isolated named Herdr session, using the current locally built artifact with matching local/remote SHA-256 | Passed |
| Cancellation rejection, failed-stop semantics, handshake identity rejection and exact-process termination | Focused portable contract tests plus the native Windows test executable | Passed |
| Rendered command lines pass every argument unchanged when executed | `sh`, `bash`, `dash` and `zsh` on Linux; `sh`, `bash`, `dash` and Windows PowerShell 5.1 on native Windows; missing shells follow the [test environment contract](contracts/testenv.md) | Passed |
| Native handle stop contract: verified termination, repeated and concurrent stops, natural exit, ended context, idempotent close, no use of a released handle | Native Windows test executable against a live helper process | Passed |
| macOS in Herdr | Not run | macOS has CI only, not live acceptance (R1.30) |
| [Conformance](#conformance) of the Claude adapter over the stub `claude`: direct child, inside the Artisan launcher (stop at rung 1) and on interpreter-rendered attempts; `artisan start --adapter claude` outcomes | CI on Linux, Windows and macOS | Run on every change |
| Claude adapter with Claude Code 2.1.306: `TestLiveClaude` (a file written and a succeeded result listing it), and an interrupt ending a working session in under a second with no result | Linux (WSL) | Passed on the earlier wrapper, before the model directory (Claude read the original prompt and wrote `<result>.draft`); pending a re-run on the current wrapper |

Reconciling an attempt after a restart is [`artisan resume`](artisan.md#resume); the per-task watcher is the owned launcher's [advance](running.md#the-watcher), and recording pane bindings is not built yet.
