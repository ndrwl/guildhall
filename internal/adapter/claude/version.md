---
description: Show which guildhall binary this session runs, the merge it ends at and how far it is behind main
argument-hint: "[startup [<startup.md path>]]"
allowed-tools: Bash(guildhall version:*)
---

Arguments: $ARGUMENTS

Run one Bash command, and nothing else first:

- when the first argument is `startup`, run `guildhall version --startup`;
- otherwise run `guildhall version`.

Reply with only the command's output, verbatim, and no commentary.
If the command fails, reply with only its error output.

When the first argument is `startup` and a path follows it, after that reply read the file at that exact path and follow it.
