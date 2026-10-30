---
description: Show one task in depth, with its timeline, attempt summaries, documents and spend (read-only)
argument-hint: "<task> | <repo>/<task>"
allowed-tools: Bash(guildhall clerk inspect:*)
---

Arguments: $ARGUMENTS

Run one Bash command, and nothing else first: `guildhall clerk inspect $ARGUMENTS`.

Reply with only the command's output, verbatim, and no commentary.
If the command fails, reply with only its error output.
