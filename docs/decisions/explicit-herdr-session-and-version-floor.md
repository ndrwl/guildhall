---
status: accepted
date: 2026-10-20
topic: herdr
formerly: D18, 0018-explicit-herdr-session-and-version-floor.md
---

# Every Herdr call names its session explicitly, and Herdr 0.9.5 is the version floor

## Context

Herdr picks its server from ambient environment variables, so a call made from inside some other Herdr session could reach the wrong server.
Only one Herdr version had been exercised live.

## Decision

Every Herdr call carries `--session <name>` and a matching `HERDR_SESSION`, with inherited Herdr context variables removed, so ambient state never chooses the server.
The session-selection and tab-ownership rules are defined in [Herdr access](../adapters.md#herdr-access).
0.9.5 is the minimum client and server version, because it is the version exercised live; an older or unparsable version is refused.

## Consequences

A user on an older Herdr must upgrade before Guildhall will use it.
Guildhall never acts in whatever Herdr session it happens to be started from.
