# D75. The Director's skills

[D57](0057-director-conversation-first.md) makes plain English to the Director the primary interface and requires the Director's skills to cover everything a Guildmaster would otherwise type.
The release now ships those skills, and a test holds them to that rule.

## Decisions

- **Five skills, by when the Director needs them.**
  `guildhall-director-startup` handles a new session's `startup.md` and ends with `director ready`; `guildhall-director-inbox` runs on every wake line and turn start; `guildhall-director-tasks` turns what the Guildmaster says into commands, including answering decisions in conversation and merging; `guildhall-director-stage` runs a [Director stage](0072-director-stages.md) and finishes it with `step finish`; `guildhall-handoff` records a handoff note and reloads.
  Splitting by moment rather than by command keeps each description precise enough for the runtime to load the right one.
- **Coverage is checked mechanically.**
  A table test reads the README's command list and fails for any command no Director skill names as `guildhall <command>`, except a short list the Guildmaster never asks the Director for, each with why: `help`, `store init`, `director start`, `artisan start`, the `fake-artisan` and `claude-artisan` wrappers, `steward rollout` and `render`.
  So a new README command is not complete until a skill can call it.
- **Skills name only real commands.**
  The CLI's help test fails for any command a release skill names that the help does not list, so a skill cannot send the Director to a command that does not exist.
  The commands of the slice not yet built, `director handoff|reload`, are listed as pending with their slice, and a pending command the help lists fails the test until it leaves the list, so each is checked against the built command when it lands.
- **`/guildhall-handoff`, not `/handoff`.**
  Claude Code invokes a skill by its name, and every release skill carries the `guildhall-` prefix, so the Guildmaster types `/guildhall-handoff`; the same directory serves the Director and the Guildmaster.
- **Notes and drafts never go to a temporary directory.**
  The upstream handoff skill saves to the operating system's temporary directory; here the note is recorded with `director handoff --note-file -` as a document of the copy, and a stage's drafts live under the session directory, so a fresh session can trust both.
- **Credits exactly where adapted.**
  Only `guildhall-handoff` of the five is adapted, from mattpocock/skills `productivity/handoff`, so only it carries `CREDITS.md`; a test now checks that a skill carries the notice exactly when its body names an upstream source.

## Not yet

The pending commands land with the handoff and reload slice, whose tests then pin the skills to them; `director pause|resume|stop` landed first and the skills follow their built behaviour, including that `stop` also stops running attempts.
A `stop-requested` message asking for a handoff note before a stop lands with handoff notes, and the skills gain it then.
No live Claude Code Director session has followed these command sequences yet; the Director [safety checks](../director.md#safety-checks) script the startup recovery and task commands over stand-in runtimes.
