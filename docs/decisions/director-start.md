# The Director's session supervisor

`guildhall director start [runtime] --foreground` is the first way to run the Director as a Guildhall session rather than a bare `claude`, as the [role grammar](0053-cli-role-grammar.md) requires.
It is the session supervisor the [session record](director-session-record.md) was designed for: it records the session, renders what the runtime reads, runs the runtime as its child and records the end.

## Decisions

- **Foreground only, for now.**
  The session runs as the supervisor's child in the current terminal, with the terminal's input and output.
  Opening the session's own Herdr tab is the next slice; until then `--foreground` is required, so the flag's meaning does not change when the tab lands.
- **Recovering starts no new work.**
  A new session is `recovering` until the Director runs `guildhall director ready`, which follows the captain's rule that a restarted Director handles what is in flight before starting anything.
  The binary enforces it at the two commands that start new work, `task new` and `task start` of a task with no attempt, and nowhere else, so recovery itself (`step done`, `step retry`, `decide answer`, `messages`, every read, and advancing a task already under way) is never blocked.
- **The startup instructions are the status feed.**
  `startup.md` lists `director status`'s `needs_me`, built by the same function, so what a new session is told and what firstmate's monitor sees cannot drift; the feed itself now lists a `recovering` session first.
- **Every release skill is rendered.**
  The Director's own skills are not written yet, so a session gets every release skill, by directory name with the first root winning, and its manifest records each tree digest, which equals the release's.
- **One session shape per runtime, beside the attempt shape.**
  `runtimes.LookupSession` is the one owner of the session runtimes, as `Lookup` is of attempt runtimes.
  Each runtime names the program it needs on `PATH`, its skills directory and its own session files; Claude Code's are `.claude/skills/` and `.claude/settings.json`, which allows `guildhall` and read-only `git` in Bash and denies edits to the copy's `config.toml` and `managed.toml`.
  The model is passed as `--model`; effort is not passed, because Claude Code reads it from its own settings.
- **The session doubles share one script.**
  Neither the fake Director nor the Claude stub calls a model: both play `guildhall` commands scripted in their role prompt with `fake.RunScript`, end at a scripted `/exit` or status, and otherwise read their input until `/exit`, so the session contract and the later CI safety checks run the same script on both.
- **Managed copies wait for managed mode.**
  Until the admission boundary lands, a copy with `managed.toml` or inline `[managed]` settings is refused, naming `guildhall director enrol`.

## Not yet

The status-line context reporter, the reload loop that starts a new session after a handoff, handoff notes in `startup.md`, the Herdr status preflight line and the Herdr tab land with the handoff, reload and tab slices.
A live Claude Code Director session has not been run; the stub proves only the command shape and settings the adapter writes.
