---
status: accepted
date: 2026-10-31
topic: steward
tasks: d102, d123
amends: steward-rollouts-copy-with-scp.md
---

# The Steward runs only a fixed allow-list of Guildhall commands over ssh

## Context

[Scp rollouts](steward-rollouts-copy-with-scp.md) ran no remote command, but managed mode needs a rollout to authorize what it published in the receiving Director's own store ([optional Steward](optional-steward.md)), and reading each copy's mode needs a remote read.
d96 named two fixed commands, `config reload` and d98's `director reload`; the d102 and d123 grills added read-only `director mode --json` as a third.

## Decision

The Steward's `Runner` runs `ssh -o BatchMode=yes -- <alias> <guildhall> <command> --copy <copy_dir>` from `PATH`, as an argument list, and nothing else.
The allow-list is exactly `config reload`, `director mode --json`, and `director reload`, reserved and refused until d98 builds it; any other command, flag or argument, and any alias that is not a plain roster alias, is refused before `ssh` runs.
`<guildhall>` is the roster's optional per-machine absolute `guildhall_path`, else `guildhall` on the account's `PATH`.
Every value keeps to the restricted character set, so POSIX shells, `cmd.exe` and PowerShell read the command `ssh` joins the same way, and nothing is quoted.
Every apply that writes a copy runs `config reload` on it, approved or routine; a failed reload is reported per copy and does not undo the copy ([remote commands](../steward.md#remote-commands)).

## Alternatives rejected

- A general remote shell: the scp record's reason stands, since quoting differs between servers and roster values are not fully trusted.
- Reloading only after approved applies: a routine apply would then publish a digest its Director refuses as unauthorized.
- Dropping the remote read and showing only the last receipt in `steward status`: it would show what the Steward sent, not what the copy applies.

## Consequences

Each planned reload is part of the plan's commands, so it is covered by the plan digest an approval names, and every plan digest changed once when this was adopted.
A machine whose `guildhall` predates `config reload` gets the new file but refuses it until a reload succeeds there.
`ssh` runs share scp's two-minute timeout and cancellation, which kills the process group on Unix and only the direct process elsewhere.
