---
status: accepted
date: 2026-11-19
topic: ci
tasks: d255, d246
---

# CI runs the Steward's takeover, collect and rollout over real OpenSSH on localhost

## Context

Every Steward suite reaches its machines through `localCopier`, `stewardtest.Fleet` or the test binary playing `ssh` and `scp`, so Guildhall's `ssh` and `scp` argument lists, host-key checking, SFTP-mode scp, `Fetch`'s "No such file or directory" detection and the remote shell's re-parsing of the command line had never met a real server (H118 of d246).

## Decision

- **A gated test, one CI step.**
  `TestLiveSSH` (`cmd/guildhall/live_ssh_test.go`, Linux only) skips unless `GUILDHALL_LIVE_SSH=1`; once set, a missing `/usr/sbin/sshd`, `ssh`, `scp`, `ssh-keygen` or `git`, or a temporary path outside the roster's restricted characters, fails it.
  A "Live SSH" step in `test (ubuntu-latest)`, after "Test Director suites managed", installs `openssh-server` when `/usr/sbin/sshd` is absent and runs only that test with the gate and `-count=1`; the `Test`, managed and race steps never set the gate.
- **The built binary on both ends.**
  The test drives the binary `sessionPath` builds and names it as the roster's `guildhall_path`, so the remote commands run the same program.
- **Throwaway identity, unprivileged sshd.**
  `ssh-keygen` makes a client and a host key in the test's temporary directory; `known_hosts` pins the host key under a fixed `HostKeyAlias` with `StrictHostKeyChecking yes`.
  `/usr/sbin/sshd -D -e -f <cfg>` runs as the test's user on `127.0.0.1` with `UsePAM no`, `StrictModes no`, key-only login and `Subsystem sftp internal-sftp`, in its own process group, which cleanup kills.
- **`-F` wrappers.**
  OpenSSH reads `~/.ssh` from the passwd home, not `$HOME`, so no environment variable keeps it away from the user's config.
  Scripts named `ssh` and `scp` go first on `PATH`; each logs its argv and runs `exec /usr/bin/<tool> -F <ssh_config> "$@"`, so Guildhall's own arguments reach OpenSSH unchanged and the log proves real tools ran after each step. `IdentityAgent none` keeps a developer's agent out.
- **The remote environment by `SetEnv`.**
  An sshd session inherits nothing of the test's, so the throwaway `sshd_config` sets `GUILDHALL_HOME`, `GUILDHALL_SECRETS_DIR`, `GIT_CONFIG_GLOBAL` and `GIT_CONFIG_NOSYSTEM=1` into the temporary directory with `SetEnv`, which needs OpenSSH 8.7 or later; the remote line execs the binary itself.

## Alternatives rejected

- A separate `live-ssh` job: branch protection requires checks by name, so it could not block a merge until the protection settings changed.
- The test in `internal/cli`, reusing `newTakeoverSetup`: less fixture code, but the far side of sshd must be a real binary either way, and `cmd/guildhall` already builds it.
- A `#!/bin/sh` wrapper as `guildhall_path` exporting the variables: it works with any sshd, but the remote line would run a script rather than the binary.
- Adding `-F`, `-i` or host-key options inside `steward`: Guildhall adds no ssh options of its own ([transport and trust](../steward.md#transport-and-trust)).

## Consequences

The test needs OpenSSH 8.7 or later on the runner, which `ubuntu-latest` has; an older sshd fails at `sshd -t`.
It covers takeover, collect and a routine rollout only; refusals, abandon, secrets, messages, provisioning, bootstrap, retirement, Windows and macOS stay on the stubbed suites.
A finding over real OpenSSH, such as scp's missing-file text changing, fails the step rather than being worked around in the test.
