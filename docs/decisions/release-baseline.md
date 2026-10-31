---
status: accepted
date: 2026-10-22
topic: release
formerly: D63, 0063-release-baseline.md
---

# Role prompts and stage guides ship with the binary

## Context

A Director copy could not run an Artisan stage until someone wrote `baseline/roles/artisan.md` and a `baseline/stages/<kind>.md` for every kind beside its config, so a new copy with only `config.toml` failed its first launch.

## Decision

The binary embeds a release baseline in a second root beside [the release skills](release-skills-root.md): the Director and Artisan role prompts and a guide for every kind an Artisan may run.
An advance resolves each role and guide from the copy's `baseline/` first, file by file, and then from the release, as skills resolve root by root, and the prompt manifest records which root each came from.
The release ships no standards, because standards are the Guildmaster's.
A kind with a guide in neither root is still refused at render time.
The content is short, runtime-neutral Markdown (R1.21) checked by Go tests like skills ([contract](../contracts/baseline.md)).
The Director role prompt carries the standing rules the Director runs under: it never does the work (R1.5), it talks to the Guildmaster in plain English and runs `guildhall` commands through its skills, it grills the Guildmaster live, and it judges every inbox message rather than obeying it.

## Consequences

A new copy runs with only `config.toml`, and a copy overrides the release one file at a time.
Changing a role prompt or guide changes the baseline digest golden and every rendered prompt that uses it.
