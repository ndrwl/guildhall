# `internal/adapter/codex`

## Contract

The Codex CLI runtime for read-only steps ([running an attempt](../adapters.md#running-an-attempt)), registered in [`runtimes`](adapter-runtimes.md) as `codex`, attempt-only.
`Adapter.Command` is `guildhall codex-artisan --task <id> --attempt <id> --prompt <file> --result <file>`, then `--copy`, `--profile`, `--repo`, `--model`, `--effort` and `--codex <program>`, each only when set, in the spec's working directory with the spec's environment.
It refuses an invalid spec, settings `CheckSettings` refuses, an unset `Executable`, and a profile whose effective grant (`claude.ProfileTools`) has any rule, since Codex runs read-only and grants none; it returns no command then.
`CheckSettings` honours a model that is one word not starting with `-`, and an effort among `Efforts`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh` and `max`; it refuses anything else with an error wrapping `adapter.ErrUnsupportedSetting` that names it.
It declares no optional capabilities.
The wrapper the command runs, `guildhall codex-artisan`, is not built yet: until it is, a `codex` attempt ends with no result.

## Tests

`codex_test.go` pins the command line with and without the optional flags, the refusals of an invalid spec, a missing executable and a bad setting, and the settings each effort and model form get.
It was written with the package and not run red.
