# D53. One command grammar for the Director, Artisan and Steward roles

The staged plan is for each role to be started, stopped, inspected and resumed with the same shape:

```text
guildhall <role> start|stop|status|resume [runtime]
```

where `<role>` is `director`, `artisan` or `steward`.
The four lifecycle verbs are reserved at the top of each role noun, and no role uses them for anything else.
`[runtime]` names the agent runtime, such as `claude`; it defaults to the runtime the copy's config names for that role, and a positional runtime overrides the config for that one command.
Role-specific flags, such as the attempt identity of `artisan start`, come after these arguments.
Flags naming the same thing spell it the same on every command: a repository is `--repo`.

`start` is what makes a role session a Guildhall one: it renders the role's prompt, skills and settings, runs the preflight, records the session's identity, and opens its Herdr tab.
A role is therefore started with `guildhall director start claude` rather than by running `claude` directly; a bare `claude` gets none of the rendered skills, settings, preflight or recorded identity, so Guildhall cannot rely on or later recover such a session.

Role-specific work stays under sub-nouns of the role or under its own top-level noun, never as a fifth lifecycle verb: `steward rollout ...`, `artisan harness ... --lab`, `task ...`, `decide ...`, `messages ...` and `workflow ...`.

## What this changes now

`artisan run` is renamed `artisan start` in the command line, help, README, docs and tests.
Guildhall is pre-release, so there is no alias: `artisan run` is a usage error, exit code 2, whose message is `artisan run is now artisan start`.
Help lists every artisan verb, `artisan start|stop|status|resume` and the lab-only `artisan harness inventory|kill-owned|probe --lab`, and the `artisan` usage error names all five.
The attempt commands' `--repository` is renamed `--repo`, the spelling of `task new|start`, `step done` and `decide`, also with no alias: `--repository` is a usage error, exit code 2, whose message names `--repo`.
Earlier decisions and evidence files that mention `artisan run` or the attempt commands' `--repository` are records of their time; current command usage is documented in [Artisan commands](../artisan.md#commands).

This slice renames `artisan run` and pins the grammar; `director start` and `steward start` are deferred to their own later slices.
`artisan start` still names its runtime with `--adapter fake`; the positional runtime and its config default arrive with those later slices, which follow this grammar.

## Existing and designed commands against the grammar

| Command | Status | Fits the grammar? | Proposed mapping |
|---|---|---|---|
| `artisan start\|stop\|status\|resume` | built | yes | none |
| `artisan harness inventory\|kill-owned\|probe --lab` | built | yes, a sub-noun | none |
| `steward rollout --dry-run` | built | yes, a sub-noun; its mode is a flag | none |
| `steward rollout --apply [--approve <digest>]` | built | yes, a sub-noun; its mode is a flag | none |
| `director enrol\|leave` | designed ([director modes](../director-modes.md)) | yes, verbs outside the lifecycle four | none |
| `director mode` | designed | yes, outside the reserved lifecycle verbs | none |

No built command clashes with the reserved verbs, so nothing else is renamed here.

`director resume` and `artisan resume` share the verb with two meanings: the Director's resume lifts a [pause](../director.md#pause-and-resume) of its session, and the Artisan's settles one attempt whose launcher is gone ([decision](0073-director-pause-stop.md)).
