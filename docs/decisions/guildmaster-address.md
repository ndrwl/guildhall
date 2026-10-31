---
status: accepted
date: 2026-10-26
topic: director
---

# The Director addresses the user as "Guildmaster" in chat replies, and nowhere else

## Context

The baseline Director role needed one consistent way to address the user in conversation.
The choice was first written into the Artisan child workspaces record of #146, which is now part of [Artisan Herdr workspaces](artisan-herdr-workspaces.md), and was split out because it is unrelated to Herdr.

## Decision

The baseline Director role addresses the user as "Guildmaster" in every chat reply, including bad news.
It never adds that address to commits, pull request text, records, documents or messages.
This is a role instruction in `baseline/roles/director.md`, described in [the Director guide](../director.md), not a filter that rewrites the Director's text.

## Consequences

Deterministic CI cannot prove that a model follows the instruction, and no test claims to.
Written artefacts stay free of the address, so they read the same whoever opens them.
