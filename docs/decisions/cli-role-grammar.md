---
status: accepted
date: 2026-10-21
topic: cli
amends: launch-composition.md
formerly: D53, 0053-cli-role-grammar.md
---

# One command grammar for the Director, Artisan and Steward roles

## Context

The Director, Artisan and Steward roles are each started, stopped, inspected and resumed, and the first owned attempt commands named their verbs and flags ad hoc (`artisan run`, `--repository`).
Pinning one shape before more role commands land keeps them from drifting apart.

## Decision

```text
guildhall <role> start|stop|status|resume [runtime]
```

- `<role>` is `director`, `artisan` or `steward`; the four lifecycle verbs are reserved at the top of each role noun, and no role uses them for anything else.
- `[runtime]` names the agent runtime, such as `claude`; it defaults to the runtime the copy's config names for that role, and a positional runtime overrides the config for that one command.
- Role-specific flags come after these arguments, and flags naming the same thing are spelt the same on every command: a repository is `--repo`.
- `start` is what makes a session a Guildhall one: it renders the role's prompt, skills and settings, runs the preflight, records the session's identity and opens its Herdr tab, so a role is started with `guildhall director start claude`, not by running `claude` directly.
- Role-specific work stays under sub-nouns of the role or under its own top-level noun (`steward rollout`, `artisan harness ... --lab`, `task`, `decide`, `messages`, `workflow`), never as a fifth lifecycle verb.

The commands checked against the grammar are listed in the [CLI contract](../contracts/cli.md#role-grammar).

## Alternatives rejected

- Keeping `artisan run` and `--repository` as aliases: Guildhall is pre-release, so each is a usage error, exit code 2, naming its replacement.
- A bare runtime session such as `claude`: it gets none of the rendered skills, settings, preflight or recorded identity, so Guildhall cannot rely on or recover it.

## Consequences

`artisan run` became `artisan start`, and the attempt commands' `--repository` became `--repo` ([launch composition](launch-composition.md)); earlier decisions and evidence that name the old spellings are records of their time, and current usage is in [Artisan commands](../artisan.md#commands).
`director resume` and `artisan resume` share a verb with two meanings: the Director's lifts a [pause](../director.md#pause-and-resume), the Artisan's settles one attempt whose launcher is gone ([pause and stop](director-pause-stop.md)).
`director start` and `steward start` came in later slices that follow this grammar.
