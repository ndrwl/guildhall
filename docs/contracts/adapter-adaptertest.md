# `internal/adapter/adaptertest`

## Contract

`RunConformance`: the launch and result protocol every runtime adapter passes through a real child process, with no model calls; `Runtime` is the narrow interface it consumes.
`Subject.Start` runs the command the way a launch path does, such as inside the Artisan launcher, as a `Process` the suite can wait for and stop; nil starts a direct child process.
`Subject.Attempt` lays out each attempt the way the launch path feeding the runtime does, such as an interpreter-rendered prompt mapped through `interp.Spec.AdapterSpec`; nil writes the behaviour's prompt as the whole prompt file.
`StartInPane` is a `Subject.Start` through `adapter.TabLauncher`: the command typed into a new tab quoted for its shell, a stop by Ctrl+C, and exit when the tab's shell is again its only foreground process.
It needs a host whose `RunInTab` returns only once the line is the pane's foreground process, as `ShellPanes` does; it is not a harness for a live Herdr server, which types the line and may still show an idle shell on the first poll.
`ShellPanes` (Unix only) is the terminal host for that where no Herdr server exists, as in CI: tabs only in its one workspace and only its own operated on, one non-empty line run as `sh -c` in the tab's directory and environment, Ctrl+C as SIGINT to the line's process group, repeated until it exits or a bounded wait ends, a line typed while another runs logged and ignored as input to that program, `Reopen` for a later host on the same panes that owns none until it adopts them, and every call honouring its context.
`RunSessionContract` is the [Director session](../director.md#start) contract every runtime passes through a real child process, with no model calls: `SessionRuntime` is the narrow interface it consumes and `SessionSubject.Script` writes the role prompt that plays a script.
Over a recording `guildhall` first on `PATH` (`RunIfRecorder`, first in `TestMain`), the session runs the script's commands in the session directory with `GUILDHALL_COPY` set to the copy, goes on past a failing one, ends with a scripted status, at a typed `/exit` and at the end of input, and an invalid spec gets no command.
Every runtime adapter runs the suite, the [Claude adapter](adapter-claude.md) included ([adapters](../adapters.md#conformance)).

## Tests

Run by `internal/adapter/fake`, `internal/adapter/claude` and `internal/adapter`; `RunSessionContract` by the first two.
`ShellPanes` passes the terminal boundary's contract test (`TestShellPanesContract` in `internal/adapter`), the same one `HerdrTabs` and its double pass.
`TestShellPanesContract` and the runs on interpreter-rendered attempts and in a pane were written red first against stubs ([red run](../evidence/adapter-conformance-2026-10-21-red.txt)); `Subject.Attempt` is suite plumbing with no test of its own.
A first version of `ShellPanes` sent one SIGINT and failed the contract test about once in twenty runs under load, when the signal reached the line before it was ready; it now repeats the signal.
