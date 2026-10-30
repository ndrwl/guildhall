---
description: Show one task's state, steps, pull request and where it stands (read-only)
argument-hint: "<task> | <repo>/<task>"
allowed-tools: Bash(guildhall clerk task:*)
---

Arguments: $ARGUMENTS

Run one Bash command, and nothing else first: `guildhall clerk task $ARGUMENTS`.

Reply with only the command's output, verbatim, and no commentary.
If the command fails, reply with only its error output.
