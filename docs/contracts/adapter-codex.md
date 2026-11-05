# `internal/adapter/codex`

## Contract

The Codex CLI runtime for read-only steps ([running an attempt](../adapters.md#running-an-attempt)), registered in [`runtimes`](adapter-runtimes.md) as `codex`, attempt-only.
`Adapter.Command` is `guildhall codex-artisan --task <id> --attempt <id> --prompt <file> --result <file>`, then `--copy`, `--profile`, `--repo`, `--model`, `--effort` and `--codex <program>`, each only when set, in the spec's working directory with the spec's environment.
It refuses an invalid spec, settings `CheckSettings` refuses, an unset `Executable`, and a profile whose effective grant ([`grant.ProfileTools`](adapter-grant.md)) has any rule, since Codex runs read-only and grants none; it returns no command then.
`CheckSettings` honours a model that is one word not starting with `-`, and an effort among `Efforts`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`; it refuses anything else with an error wrapping `adapter.ErrUnsupportedSetting` that names it.
It declares no optional capabilities.
Its [runtime capabilities](adapter-runtimes.md): `GrantsTools` and `SkillPolicy` are false, and `adapter.ReadOnly` declares it read-only; `Signin` is Codex's `guildhall signin` entry, `codex login status`, then `codex exec --skip-git-repo-check --sandbox read-only <adapter.SigninPrompt>`.

### The wrapper

`Main` is `guildhall codex-artisan`: `ParseArgs` takes the flags `Command` passes, refusing (exit 2) missing or relative prompt and result files, the same file for both, a relative `--codex` and settings `CheckSettings` refuses; `Run` returns exit 0 or, failing, 1.
It runs under `Answers`, the answer contracts by workflow step, which the caller supplies; `guildhall` supplies `cli.CodexAnswers`, the [quality judge's](quality.md) `quality.CodexAnswer` alone.
An `Answer` names the document the step answers with (`File`, in the working directory), its strict JSON Schema and a `Check` given the prompt's directory.

`Run` refuses, starting nothing, a profile granting any rule, an attempt whose step (`artisan.ParseAttemptID`) has no answer contract (`ErrNoAnswerContract`), a cancelled attempt (`adapter.ErrAttemptCancelled`) and an existing result (`adapter.ErrResultExists`).
It then checks the installed Codex, `codex` on `PATH` or `--codex`, before running it:
- `codex --version` must print `codex-cli X.Y.Z`, perhaps with a suffix, at least `MinVersion`, 0.163.0; an older or unparseable one is `ErrVersion`, and the version is named on stderr, the launcher log;
- `codex login status` must exit 0 reporting `Logged in`, the machine's existing login, or it is `ErrNotLoggedIn`;
- the user's configuration, `config.toml` in `$CODEX_HOME` or `~/.codex`, must declare no `mcp_servers`, which run outside the sandbox, or it is `ErrUserMCPServers` naming them; a missing file declares none and an unparseable one is refused;
- `codex exec --help` must list each of `ExecFlags`, or it is `ErrMissingFlag` naming the missing ones; `--output-schema` is passed only when it lists it.

The run is `codex ExecArgs`: `exec --json --ephemeral --skip-git-repo-check --sandbox read-only -C <working directory>`, then `Switches` on every run, `--disable apps`, `-c web_search="disabled"`, `-c mcp_servers={}` and `-c sandbox_workspace_write.network_access=false`, then `-m <model>` and `-c model_reasoning_effort=<effort>`, each only when set, `--output-schema` with `OutputSchema` written beside the result as `codex-output-schema.json`, and `Instruction` as the prompt, with no standard input.
`Instruction` names the prompt file, says the sandbox is read-only, and asks for the final message as one JSON object, `status`, `summary` and `answer`, the step's document or null when failed, in place of any file the prompt asks for.

The `--json` stream is decoded as it arrives: each agent message and each command or MCP, web-search or file-change tool call is a line on stdout and a Workbench event (`WorkbenchPath`), each command's completion a tool result carrying its real `exit_code`, and an `error` event, failed turn or error item is named on stderr; unknown events and fields are ignored.
The last agent message is the final message, kept beside the result as `codex-final-message.txt`; usage is the summed `turn.completed` usage, input `input_tokens − cached_input_tokens` floored at 0, cache read `cached_input_tokens`, cache creation `cache_write_input_tokens` and output `output_tokens`, measured with an unknown cost, and unknown when no turn reports any.

An interrupt, the end of the context or the cancel marker stops Codex, killing it after `StopGrace`, and publishes nothing; the marker is not an error.
Once Codex exits, `Run` writes the usage file, then publishes only for a run whose last turn completed and whose final message decodes exactly, a ```` ```json ```` fence aside:
a `failed` message publishes a failed result with no outputs and writes no document; a `succeeded` one with an answer the contract's `Check` accepts writes the indented answer as `File` in the working directory, then publishes a succeeded result naming it as its one output, by `adapter.WriteResult`, exactly once.
A failed turn, an error with no completed turn, no final message, one that does not decode, or a succeeded one with no answer or an answer `Check` refuses publishes nothing and returns `ErrNoAnswer` naming why.
Codex's exit status does not decide completion: a failing exit after a published result is still an error.

## Tests

`codex_test.go` pins the command line with and without the optional flags, the refusals of an invalid spec, a missing executable and a bad setting, and the settings each effort and model form get.
`wrapper_test.go` runs `adaptertest.RunConformance` over the [`codextest`](adapter-codex-codextest.md) stub on judge attempts, with `Subject.WritesResult`, and runs the shipped wrapper, under the judge's contract, to show: the read-only, apps, web-search, MCP and network switches, model, effort and output schema on the command line, and their absence or presence as settings and `codex exec --help` decide; `judgement.json` written and accepted by `quality.CheckAnswer`, plain and fenced, with the succeeded result, the mapped usage, the version on stderr and progress on stdout; Workbench tool results with exit codes 0 and 1; unknown usage without a usage block; a failed result with no document; nothing published for a malformed message, an answer for another self-review, a null answer, no final message and a quota error with a failed turn, which is named on stderr; refusals before `codex exec` runs for an older and an unparseable version, no login, a user MCP server, a missing flag and a step with no contract; the version floor's accepted forms; refusals of a cancelled attempt and an existing result; and a stop on the cancel marker that publishes nothing.
They were written with the wrapper and not run red; the first run found the decoder's stderr lines lost to `os/exec` copying Codex's stderr into the same buffer, now serialized.
