# Sign-in

Each Director machine signs into the runtimes it uses itself: Claude Code, Codex and `gh` for the forge.
A sign-in stays in each runtime's own store (R1.10, R1.28), and Guildhall never stores, copies or prints one.
`guildhall signin` is the sign-in step of the setup wizard (R1.44, R1.47, R1.48), built as a standalone command, which machine provisioning later runs instead of building its own.

```sh
guildhall signin [--check] [--copy <dir>] [claude|codex|gh]...
```

It handles each runtime named, in the order claude, codex, gh; with none named, each runtime the [copy](running.md#copies) from `--copy` or copy discovery uses; with no copy found, all three.
A copy uses Claude Code or Codex when its session runtime, its `[director]` or `[steward] adapter` or any profile's adapter runs it, and `gh` when it is a Director copy, since a Director publishes through `gh`.
A Steward copy needs no `gh`.

## Check, act, verify

For each runtime it runs three steps (R1.48):

- **Check** probes the sign-in: the runtime is on `PATH`, its own status command says signed in, and then a minimal real call succeeds.
  A status alone is not trusted, because a Claude Code token can pass `claude auth status` and then fail every real call with 401.
  It prints `<runtime>: check: ` and what it found.

  | Runtime | Status | Real call |
  |---|---|---|
  | `claude` | `claude auth status` | `claude --print "Reply with the single word OK." --max-turns 1` |
  | `codex` | `codex login status` | `codex exec --skip-git-repo-check --sandbox read-only "Reply with the single word OK."` |
  | `gh` | `gh auth status --hostname github.com` | `gh api user --jq .login` |

  A probe runs in a new empty directory, so no copy's project settings or hooks load, and `gh` runs with `GH_PROMPT_DISABLED=1`.
  The status command is bounded at 30 seconds and the real call at two minutes; a model call that prints nothing has failed.
  Codex keeps its login in a file or in the keyring, as `cli_auth_credentials_store` says, so the probe asks `codex login status` and never looks for `~/.codex/auth.json`.
- **Act**, when the check fails, prints the exact command and link and waits for the Guildmaster to run it in their own terminal (R1.47); Guildhall runs and opens nothing:
  - Claude Code: `claude auth login`, or start `claude` and type `/login`; <https://code.claude.com/docs/en/authentication>.
  - Codex: `codex login --device-auth`, which needs device-code sign-in turned on for the ChatGPT account first, in ChatGPT's Settings > Security (a workspace admin turns it on for a workspace); <https://developers.openai.com/codex/auth>.
  - GitHub: `gh auth login --hostname github.com`, then `gh auth setup-git`, so `git` pushes use the same sign-in; <https://cli.github.com/manual/gh_auth_login>.

  Each says never to copy a login from another machine, such as `~/.codex/auth.json` or a Claude token.
  It then prints `<runtime>: press Enter once signed in, or type skip` and reads one line.
- **Verify**, after Enter, runs the check again, real call and all, and prints `<runtime>: verify: ` and what it found.
  `skip`, or the end of the input, leaves the runtime not signed in.

With `--check`, it only checks, printing what it found for each runtime and never acting.
It exits 0 when every runtime handled is signed in at the end, and otherwise 1, writing `guildhall: signin: <n> of <m> runtimes not signed in` to stderr.

## No token is printed

No line holds a probe's output: a probe that fails is described by its exit status or its timeout only.
The one value printed is the GitHub login `gh api user` returns, `signed in as <login>`, which is not a secret, and only when it has the shape of a GitHub login.

## In the start preflight

[`director start --check`](director.md#start) and [`steward start --check`](steward.md#start) run the same probes, report only, after the rest of the preflight passes.
They print one line per runtime the copy uses: `signin <runtime>: signed in; a real call succeeded`, or `signin <runtime>: warning: <what was found>; run guildhall signin`.
A runtime not signed in never refuses the check, and a start without `--check` runs no probe.
A copy whose runtimes need no sign-in, such as a Steward on the fake runtime, prints `signin: no runtime of this copy needs a sign-in`.

A [reload](director.md#reload) pre-flights a new binary with `start --check` and sets `GUILDHALL_SIGNIN_CHECK=skip` for it, so a reload spends no model call; the preflight then prints `signin: skipped (GUILDHALL_SIGNIN_CHECK=skip)`.

## Testing

Tests never reach a real `claude`, `codex` or `gh`: they put fake ones first on `PATH`, over the [guards](contracts/testenv.md) for `claude` and `codex`, and a fake's output holds a token-shaped string no probe line may contain.
A sign-in is not claimed to work on a platform until the Guildmaster has signed a real Linux machine in with Claude Code, Codex and `gh` through `guildhall signin` (R1.27).
