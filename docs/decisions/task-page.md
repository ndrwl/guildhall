---
status: accepted
date: 2026-10-31
topic: director
tasks: d101, d105, d122
---

# The Guildmaster's task pages are served by the Director's session supervisor, on the tailnet only, from Clerk's readers

## Context

The Guildmaster asked for every decision to be "simply a web link that I can click at any time", on one page per task with its full context and status, usable on a phone (d101, d105, d122).
R1.29 allows no always-on Guildhall service, and the copy's store has a single writer.
The Guildmaster approved d105's design-3 and plan, and asked for two slices: read-only pages first, then answering.

## Decision

`internal/web` serves one page per task and a reader page per document ([user doc](../web.md), [contract](../contracts/web.md)).
It runs only while a Director session runs, from the Director role's `watch` hook, binding again at every session and closing before the session's end is recorded, or from a hand-run `guildhall web`; a Steward session serves nothing, and a bind failure only warns.
The optional `[web]` table names the address per copy ([configuration](../configuration.md#the-web-table)); the server binds only when every address its host resolves to is in Tailscale's ranges, so it fails closed, and anyone on the tailnet can read.
`internal/web` never opens the store: a `Source` filled by `internal/cli` from Clerk's readers opens it per request with a bounded wait, reads, closes, and only then is the page rendered, so the page and Clerk agree and Clerk's output is unchanged.
Its asks are the task's share of Clerk's Needs you split.
Answer tiles say what each option does from one option-meanings table in `internal/web`, which a test keeps covering every option set the engine, the questions, grants and the Steward export.
Pages carry no script and a CSP that allows no external request; HTML documents are shown only in a sandboxed frame.
In the first slice every ask is read-only.

## Alternatives rejected

- An always-on service: R1.29 forbids it.
- Binding on all interfaces with an access check: one mistake exposes the copy to the LAN; tailnet-only binding fails closed.
- Moving Clerk's readers into a shared package: it risks Clerk's byte-for-byte output, which the seam keeps.

## Consequences

No session, no pages, unless `guildhall web` is run by hand.
The option-meanings table must change with every new option the engine offers, which the coverage test enforces.
`[web]` is strictly decoded, so it is added to a copy only after the binary that knows it is installed.
