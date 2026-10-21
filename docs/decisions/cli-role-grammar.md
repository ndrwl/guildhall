# One command grammar for the Director, Artisan and Steward roles

Each role is started, stopped, inspected and resumed with the same shape:

```text
guildhall <role> start|stop|status|resume [runtime] [--check]
```

where `<role>` is `director`, `artisan` or `steward`.
The four lifecycle verbs are reserved at the top of each role noun, and no role uses them for anything else.
`[runtime]` names the agent runtime, such as `claude`; it defaults to the runtime the copy's config names for that role, and a positional runtime overrides the config for that one command.
`--check` runs the command's preflight only: it reports what the command would prepare and refuse, changes nothing and starts nothing.
Role-specific flags, such as the attempt identity of `artisan start`, come after these arguments.

`start` is what makes a role session a Guildhall one: it renders the role's prompt, skills and settings, runs the preflight, records the session's identity, and opens its Herdr tab.
A role is therefore started with `guildhall director start claude` rather than by running `claude` directly; a bare `claude` gets none of the rendered skills, settings, preflight or recorded identity, so Guildhall cannot rely on or later recover such a session.

Role-specific work stays under sub-nouns of the role or under its own top-level noun, never as a fifth lifecycle verb: `steward rollout ...`, `artisan harness ... --lab`, `task ...`, `decide ...`, `messages ...` and `workflow ...`.

## What this changes now

`artisan run` is renamed `artisan start` in the command line, help, README, docs and tests.
Guildhall is pre-release, so there is no alias: `artisan run` is a usage error, exit code 2, whose message is `artisan run is now artisan start`.
Help lists every artisan verb, `artisan start|stop|status|resume` and the lab-only `artisan harness inventory|kill-owned|probe --lab`, and the `artisan` usage error names all five.
Numbered decisions and evidence files that mention `artisan run` are records of their time and are not rewritten.

`artisan start` still names its runtime with `--adapter fake`; the positional runtime, its config default and `--check` arrive with the slices that build `director start` and `steward start`, which follow this grammar.

## Existing and designed commands against the grammar

| Command | Status | Fits the grammar? | Proposed mapping |
|---|---|---|---|
| `artisan start\|stop\|status\|resume` | built | yes | none |
| `artisan harness inventory\|kill-owned\|probe --lab` | built | yes, a sub-noun | none |
| `steward rollout --dry-run` | built | yes, a sub-noun; its mode is a flag | `steward rollout plan` |
| `steward rollout --apply [--approve <digest>]` | built | yes, a sub-noun; its mode is a flag | `steward rollout apply [--approve <digest>]` |
| `director enrol\|leave` | designed ([director modes](../director-modes.md)) | yes, verbs outside the lifecycle four | none |
| `director mode` | designed | yes, but overlaps `director status` | report the mode in `director status` and drop `mode` |

No built command clashes with the reserved verbs, so nothing else is renamed here.
The Steward rollout mapping, which turns the `--dry-run` and `--apply` flags into `plan` and `apply` sub-verbs to match the record's `steward rollout plan|apply`, changes Steward tests, docs and usage text and is left to the `steward start` slice.
The `director mode` mapping is for the slice that builds the Director commands.
