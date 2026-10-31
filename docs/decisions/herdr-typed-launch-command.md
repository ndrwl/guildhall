---
status: accepted
date: 2026-10-20
topic: herdr
formerly: D20, 0020-typing-the-launch-command-into-a.md
---

# Guildhall types the launch command into a new Herdr tab, quoted for the pane's shell

## Context

Herdr's tab creation does not take a command to run.

## Decision

Guildhall creates the tab, waits until its shell is idle, and types one quoted command line with `pane run`.
Quoting is chosen from the shell Herdr reports for the pane: POSIX single quoting, or PowerShell's call operator with single quotes.
An unrecognised shell is refused rather than guessed.

## Alternatives rejected

- Guessing the quoting for an unrecognised shell: a wrong guess could run a different command.
- Altering an argument a shell would change: it is refused instead, so what runs is exactly what was asked.

## Consequences

Rendered lines are proven by executing them in each supported shell installed on the test host.
Supporting a new shell means adding its quoting and its test.
