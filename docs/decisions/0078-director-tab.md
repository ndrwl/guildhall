# D78. The Director's Herdr tab

The Director is a long-running conversation the Guildmaster and firstmate come back to, so it needs its own visible tab in Herdr (R1.37) rather than whichever terminal ran `director start`.
The [session supervisor](0071-director-start.md) already runs a session in the foreground; this slice puts that supervisor in a tab Guildhall creates.

## Decisions

- **The wrapper types the supervisor into the tab.**
  `director start --tab` opens the tab and types `guildhall director start --copy <dir>` with the tab's pane binding, quoted for the pane's shell by `herdr.ShellCommand` ([D20](0020-typing-the-launch-command-into-a.md)), as `artisan start --herdr-session` types an Artisan.
  The supervisor is then the tab's own foreground process, with the tab's terminal for the runtime, and nothing Herdr-specific runs inside it except recording the binding it was given.
  The wrapper waits for the session record that carries the pane, as `launch.Process` waits for ownership, and fails once the tab's shell, seen running the command, is back at its prompt with no such record, so a refused preflight in the tab is reported rather than waited out.
- **The pane binding is passed, not discovered.**
  The supervisor records the session, workspace, tab and pane the wrapper passes on four flags, which go together and never with `--tab`.
  A pane cannot reliably name itself across Herdr versions and shells, and the wrapper already holds the ids from creating the tab.
- **One workspace per Director, found by label.**
  The workspace is the one labelled with the Director's `name`, created without focus when the session has none, so restarts reuse it.
  Workspaces carry no ownership: the terminal boundary gains `Workspaces` and `CreateWorkspace`, and `adapter.EnsureWorkspace` lists before it creates.
  Two wrappers racing can each create one; the next start reuses the first listed.
  The boundary's three hosts (`HerdrTabs` over a stub Herdr process with the recorded response shapes, the in-package double and `adaptertest.ShellPanes`) run the workspace cases under the shared contract test.
- **A live session is refused; a dead one is superseded.**
  Both the wrapper, before it opens a tab, and the supervisor, in the transaction that records the new session, refuse with `director: live` while the newest session is not stopped and its supervisor's identity probes alive or cannot be shown gone.
  The contract names `running` and `recovering`; `paused` and `stopping` are refused the same way, because their supervisor and runtime still run.
  A session whose supervisor is gone is recorded `stopped` with the store-set end reason `superseded`, so `director status` and the Guildmaster can tell a crash from a clean end.
- **The tab is never closed here.**
  R1.54 allows Guildhall to close tabs it created, but a failed start's output is the evidence of what went wrong, and whether `director stop` closes the tab is its own decision.
- **The Herdr session is configured, never `default`.**
  `[local] herdr_session` names it, refused when it is `default` or not a Herdr session name, so the wrapper never opens tabs in the Guildmaster's own terminal session by accident.
  Without it the wrapper is refused, naming the setting and the session in the current terminal.
- **The tab is opt-in.**
  The Guildmaster chose the current terminal as `director start`'s default, since that is what a user typing it is likely to want; `--tab` opts into the wrapper, and the line it types needs no flag of its own because the default is the supervisor in the tab's terminal.

## Not yet

The lab checklist in [the Director](../director.md#the-directors-tab) (zsh on WSL, Git Bash and PowerShell 5.1 on native Windows, in an isolated named Herdr session) is the acceptance and has not been run.
A live Claude Code Director has not been run in a tab.
R1.37 asks for the Director's session to be its workspace's first tab, but Herdr gives a new workspace a first tab of its own, so on a new workspace the Director's tab comes second.
Closing that first tab once the Director's exists would meet R1.37, and R1.54 allows it because Guildhall created the workspace, but what Herdr does on closing a workspace's first tab is unproven, so it waits for the lab.
`director status` does not yet probe the supervisor's liveness.
