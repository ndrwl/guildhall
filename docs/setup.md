# Setup: step files, the doctor and the wizard

`guildhall setup` checks a machine against **step files** and closes what is missing: the engine closes the fixable gaps itself, and a person closes the human ones through a numbered wizard.
It is the engine d128's Steward provisioning runs on each machine, and the Guildmaster can run it by hand ([decision](decisions/setup-engine-from-firstmate.md)).
Its behaviour is ported from Firstmate's `wizard-template.sh`/`.ps1`, `fm-remote-doctor.sh` and `fm-install-herdr.sh` ([`NOTICE`](../NOTICE)).

## Commands

```sh
guildhall setup doctor                                  # every built-in step file, as text
guildhall setup doctor --steps herdr --json             # one step file, as guildhall.doctor-report/1
guildhall setup doctor --fix --only herdr --steps herdr --set herdr_version=0.5.1 \
    --set herdr_url=https://… --set herdr_sha256=<hex>  # close one fixable gap
guildhall setup wizard --steps sign-in                  # walk through the human steps
guildhall setup wizard --steps sign-in --dry-run        # show the cards; run and write nothing
guildhall setup run --steps go --set go_version=1.27.1 --set go_url=… --set go_sha256=<hex>
```

- `--steps` names a built-in step file or a path. A path needs `--digest`, the sha256 of the file's bytes, and is refused under any other; a built-in may be given one and is refused under another.
- `--only` takes comma-separated step ids (it may be repeated); with no `--only` every step for this operating system is taken.
- `--set <name>=<value>` gives a value for a param the step file declares; a name no step file declares is refused.
- `--setup-dir` is where the engine keeps its journal, completion records and ownership manifest; by default `setup/` in the Guildhall home (`GUILDHALL_HOME`, else `~/.local/share/guildhall`, or `%LOCALAPPDATA%\Guildhall` on Windows).

**`setup doctor`** runs each step's check and reports it `ok` or as a **gap**, tagged `fixable` or `human`, with the exact command line that closes it, and a `skip` for a step not for this operating system.
It changes nothing and is safe to re-run.
Text output exits 1 while any gap is left and 0 with none; `--json` (which needs `--steps`) prints the report and exits 0 whatever it holds.
`--fix` acts on fixable gaps only, every one or those `--only` names, and then re-derives every check and prints the fresh report; naming a human step is refused before anything runs, and a failed step exits 1 with the report still printed.

**`setup wizard`** walks the chosen steps as numbered cards, `Step 2 of 3 · Sign in to Codex [human]`.
A step whose check already passes is done; a fixable step is acted on and verified as `--fix` does; a human step shows its instructions, asks its prompts, runs its `run` command, if it has one, with the terminal attached, and is verified with a fresh check.
A step that fails shows its **undo note** and stops the wizard, exit 1.
`--dry-run` shows every card with what would be checked, run and asked, and writes nothing.

**`setup run`** takes the steps in order: fixable steps are checked, acted on and verified, and the first human step not done stops the run with `waiting <id>` and the wizard command that closes it.

## Step files

A step file is strict TOML; an unknown key, a missing field or a `${name}` neither declared nor set by the engine is refused.

```toml
schema = "guildhall.steps/1"
name = "herdr"                    # a lowercase id
title = "Herdr, the terminal host"

[params]                          # values the caller sets with --set, with their defaults
herdr_version = ""

[[step]]
id = "herdr"                      # the gap id the doctor reports, unique in the file
title = "Install Herdr into ~/.local/bin"
tag = "fixable"                   # or "human"
os = ["linux", "darwin"]          # of linux, darwin, windows
check = ["herdr", "--version"]    # passes on exit 0 ...
expect = "${herdr_version}"       # ... with this in its output, when set
creates = ["${home}/.local/bin/herdr"]
undo = "Remove ~/.local/bin/herdr."

[step.fetch]                      # or action = [...]
url = "${herdr_url}"
sha256 = "${herdr_sha256}"
dest = "${home}/.local/bin/herdr"
```

- **check, act, verify.** Every step's check runs first, and a passing check skips the act. `verify`, when given, replaces the fresh run of the check that follows the act.
- **Commands** (`check`, `action`, `verify`, `run`) are argv lists run with no shell, or a `{shell = "sh" | "pwsh", script = "…"}` table: that is how a step file carries a custom installation script.
  `${name}` is substituted in argv elements, `expect`, `creates` and `fetch`; a script is not substituted and reads every value from the environment as `SETUP_<NAME>`, such as `SETUP_HOME`.
  The engine sets `home`, `setup_dir`, `os`, `arch` and `exe` (`.exe` on Windows, else empty).
- **A fixable step** acts with exactly one of `action` and `fetch`.
  `fetch` downloads `url` (https, or http to the loopback address), refuses it unless its sha256 is `sha256`, then puts it at `dest`, or with `unpack = "tar.gz"` unpacks it there, dropping `strip` leading path elements; an entry that is not a file or directory, or that leaves `dest`, is refused.
  An earlier install at `dest` is replaced only after the new one is fully unpacked, and is put back if the replacement fails.
- **A human step** has `instructions`, `[[step.prompt]]` entries (`kind = "confirm" | "ask" | "ask_secret"`, `name`, `text`) and an optional `run`.
  A confirm answered no fails the step. An `ask` answer is recorded; an `ask_secret` answer is read without echo, given to `run` only as `SETUP_<NAME>`, and never written: the journal and record name the prompt and redact its value from any output they keep.
  The engine never runs `sudo`: a step that needs administrator rights is a human step.
- **Digest.** A step file's digest is the lowercase hex sha256 of its bytes, so one changed byte is another file.

## Built-in step files

| Name | Steps | |
| --- | --- | --- |
| `director` | `copy-dir` (fixable), `guildhall-pin`, `tailscale-ssh` (human) | A new Director copy's directory and its `state/steward/setup`, from `copy_dir`; the `guildhall` program printing release `guildhall_pin` as `guildhall version`; and no check-mode rule (`holdAndDelegate` in `tailscale debug netmap`) when Tailscale serves SSH on the machine, since the Steward's batch-mode ssh cannot pass one. A Steward's [provisioning](steward.md#provisioning) sets the values; with no `copy_dir` or `guildhall_pin` those steps pass, and `tailscale-ssh` passes where Tailscale does not serve SSH. |
| `herdr` | `herdr` (fixable), `herdr-session` (human) | Herdr in `~/.local/bin` from `herdr_url`, checked against `herdr_sha256`, at `herdr_version`; then, given `herdr_session`, `herdr status --json --session <name>` reporting that session's server running. A running Herdr server is never started, stopped or replaced: a person starts it. |
| `new-copy` | `copy-free`, `clone-paths-free` (human) | No `config.toml` or `director.store` in `copy_dir`, and each of `clone_paths` (separated by `:`) absent or an empty directory. A provisioning plan runs it before it acts and before each clone, and refuses with `ErrCopyExists` on any gap, so its gaps never become plan steps; with no values both pass. |
| `go` | `go` (fixable), `go-on-path` (human) | Go unpacked into `~/.local/go` from `go_url` and `go_sha256`, then put on `PATH` by hand, ahead of any other Go of another version. |
| `git-and-claude` | `git`, `gh`, `claude` (human) | Administrator installs of Git and the GitHub CLI, and Claude Code. |
| `sign-in` | `claude-sign-in`, `codex-sign-in`, `gh-sign-in` (human) | Each runtime's own status command as the check; the wizard prints the sign-in command, never a token ([sign-in](signin.md)). |
| `wsl-settings` | `wsl-systemd`, `wsl-keep-running` (human) | systemd in `/etc/wsl.conf` and `vmIdleTimeout=-1` in `%UserProfile%\.wslconfig`; outside WSL both pass. |

Step ids are unique across the built-ins, so a gap id names one step.
The built-ins are Linux and macOS forms; d128's Windows slice adds their Windows forms.
The doctor checks only what its step files check; the Guildhall facts a [provisioning](steward.md#provisioning) plan needs are steps of the built-ins: the copy directory (`copy-dir`), store and clone paths (`new-copy`), the Herdr server and `herdr_session` (`herdr-session`), Tailscale SSH check-mode (`tailscale-ssh`) and the Guildhall release against the pin (`guildhall-pin`).

## The setup directory

| File | What it holds |
| --- | --- |
| `journal.jsonl` | One JSON line per thing the engine did: a step's act, done, failed, already done, refused, waiting, and each prompt answered (no secret value). |
| `records/<name>-<digest12>.json` | The latest wizard completion record (`guildhall.wizard-record/1`) for that step file: its digest, step ids, overall status and each step's status and recorded answers. The record is a receipt; the doctor decides whether a gap is closed. |
| `manifest.json` | The ownership manifest (`guildhall.setup-manifest/1`): each file a step's `creates` or `fetch` made, with its step file, digest and step. The engine refuses to write over an existing file the manifest does not list (`ErrNotOwned`), and names it so a person can move it aside. |

The engine owns the setup directory and the files in its manifest, and nothing else.
