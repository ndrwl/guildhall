# The Director

The Director is the agent that runs a Director copy: it gives Artisans work, answers their messages and asks the Guildmaster for decisions ([architecture](architecture.md)).
This page describes the commands that serve the Director itself rather than one task.
`director status`, the [session record](store.md#director-sessions), the session supervisor `director start` that writes it, the session's [Herdr tab](#the-directors-tab), `director ready`, `director pause`, `director resume`, `director stop`, `director note`, `director follow-up`, `director reload`, `director turn-started`, `director turn-ended`, the [automatic handoff](#automatic-handoff) triggers with `director context-report`, and the standalone [guard rails](#guard-rails) are built.
A message to the Director [wakes it](messaging.md#waking-the-director) when its session is running in a pane Guildhall created.

## Start

```sh
guildhall director start [runtime] [--check] [--copy <dir>]
```

`director start` is the session supervisor: it runs one Director session in the current terminal and records it ([decision](decisions/0071-director-start.md)).
With `--tab`, `director start` instead opens the Director's own [Herdr tab](#the-directors-tab) and runs the supervisor there.
It follows the [role grammar](decisions/0053-cli-role-grammar.md): the runtime is the positional argument, else the config's [`[director] adapter`](configuration.md#the-director-table), and a copy without either is refused with exit 1 listing the adapters.
It needs a [copy](running.md#copies), from `--copy` or found by copy discovery from the current directory, because the session lives in it.
So the Guildmaster can restart the Director by typing a bare `guildhall director start` in the Director's tab, whose shell is in the copy.
A bare restart records no pane binding, because Guildhall ignores Herdr's ambient pane variables: wakes then come only through the [`wait-wake` hook](#the-end-of-a-turn), and a later start does not [retire](#the-directors-tab) that tab.
This is an accepted limitation; `director start --tab` records the binding.

It first runs a preflight and prints one line per check, as `doctor` prints, stopping at the first that fails:

- `config`: the copy's `config.toml` is a Director config.
  Until managed mode lands, a copy with `managed.toml` beside its config, or with inline `[managed]` settings, is refused naming `guildhall director enrol`.
- `runtime`: the runtime is one of the adapters, and the program it runs is on `PATH`; the fake runtime is the `guildhall` executable itself.
- `guildhall`: `guildhall` is on `PATH`, because the session's hooks, status line and Director commands invoke that bare name; a refusal names adding the running executable's directory to `PATH`.
- `store`: the copy's `director.store` exists and opens; a missing one is refused naming `guildhall store init`.
- `skills`: the release skills and baseline install.
- `artisans`: names this Director and its config file, then each Claude profile's [effective grant](adapters.md#artisan-permissions) by source: `grants from the copy` its own [`allowed_tools`](configuration.md#profiles), or `grants the shipped default`, or, for a profile of another name with no list, `gets no shipped default`; then `plus for repository <id>` each repository grant's additions.
  `acceptEdits` itself grants edits only; inherited settings, hooks and skill-scoped grants may allow more, so an empty profile grant is not a tool sandbox ([Artisan permissions](adapters.md#artisan-permissions)).
  It refuses a profile of another adapter that sets `allowed_tools` or has a repository grant, since only the claude adapter grants them, and a profile whose `model` or `effort` its runtime cannot honour, naming the profile and the setting ([runtime settings](adapters.md#runtime-settings)).
- Additional `skills` lines report the resolved [Claude skill policy](configuration.md#claude-skill-policies) for the Director and every Claude profile: preset, key, default/managed/local provenance, overridability, digest, known kept/dropped names, bundle mechanism, account sync and disabled plugin IDs.
  Release skill count is separate: 13 Guildhall release skills installed does not mean 13 total visible skills.
  Preflight probes `claude --version`, requires at least the tested 2.1.310 baseline, and refuses an inherited `CLAUDE_CODE_DISABLE_BUNDLED_SKILLS` kill switch when the policy keeps bundled skills.
  These checks validate the generated settings JSON, explicit switches and normalized control digest without launching models or Herdr, not live discovery: unknown feature/account/plugin inventory and organization-managed constraints are explicitly unobserved.
  Organization-managed force-enablement can defeat plugin disablement; preflight does not claim to inspect or override that policy.
  Selective bundle/account opt-ins are open-world, and ambient personal/project/plugin skills are not isolated.

See [Claude skill policies](configuration.md#claude-skill-policies) for the Director's shipped defaults and customization controls.
Generated settings retain existing permissions, turn hooks and status line and are passed explicitly with `--settings` so account-sync control uses a supported session scope.
The numbered session manifest owns a `claude-skill-policy.json` provenance receipt alongside its runtime files.
Persist customizations in Guildhall configuration, not the regenerated workspace; tool permissions and engine-owned review/publish authority remain the boundary.

A failed check is exit 1 with nothing recorded and nothing written in the copy, and `--check` exits 0 after the preflight, also recording nothing.

A start is refused with exit 1, naming `director: live` and recording nothing, while the newest session is not `stopped` and its supervisor's recorded identity names a live process, or cannot be shown gone.
It is refused the same way while the newest session's [recorded runtime unit](#the-runtimes-record) is not proven empty, naming the processes that survive and `director stop`, which ends them.
A session whose supervisor is gone, such as one whose machine restarted, is superseded instead, once its runtime's unit is empty.
A superseded session's emptied runtime unit is then removed, such as its leftover cgroup directory on Linux; a failed removal is a warning naming the unit and the error, `director start: warning: session <n>'s runtime unit <name> was not removed: <error>`, and the start goes on.
A unit already removed counts as removed, with no warning.
A start is refused with exit 1 the same way, naming `director: stop sweeping`, while the newest session's [stop](#stop) has not finished stopping its attempts: the session is `sweeping`, or `stopping` with its supervisor gone; wait for `director stop` to finish, or run it again to finish the sweep.

Then it records session `<n>`, one more than the newest, as `recovering` with its own process identity, release and config digest, which records an earlier session still recorded live as `stopped` with the end reason `superseded`.
`--herdr-session`, `--herdr-workspace`, `--herdr-tab` and `--herdr-pane`, given all together and never with `--tab`, are the pane binding the [tab wrapper](#the-directors-tab) passes; the session records them, and a [wake](messaging.md#waking-the-director) types into that pane.
Without those flags and without `--tab`, a start run inside a Herdr pane binds that pane from its environment: when `HERDR_SESSION`, `HERDR_WORKSPACE_ID`, `HERDR_TAB_ID` and `HERDR_PANE_ID` are all set, the session is not Herdr's `default` one and each value has the shape Herdr's client accepts, the session records that pane marked `adopted`, and the start prints `pane: <pane> (tab <tab>, workspace <workspace>, from HERDR_*)` before recording it; a [reload](#reload) keeps it.
The flags always win over the environment, and `--tab` never binds from it, since the wrapper runs in the Guildmaster's own pane.
Guildhall did not create an adopted pane's tab, so it never [retires](#the-directors-tab) it.
A session recorded with no pane gets no wake (it is queued `no pane`), so such a start first writes one warning line to stderr, `director start: warning: this session has no Herdr pane (<reason>), so messages will not wake the Director; start it with --tab or inside a Herdr pane`, where `<reason>` is such as `HERDR_PANE_ID not set` or `HERDR_SESSION is "default"`, and [`director status`](#status) lists a `no_pane` item while it runs.
It renders the session into `<copy>/director/session-<n>/` with [`render.Session`](rendering.md#director-sessions):

- `role.md`: the role prompt, from `[director] role` relative to the config directory, else the copy's `baseline/roles/director.md`, else the release's;
- every release skill, byte for byte, in the runtime's skills directory, `.claude/skills/` for Claude Code;
- the runtime's own files: for Claude Code, `.claude/settings.json`, which lets Bash run `guildhall` and read-only `git` commands without asking, but denies the internal launch commands `guildhall claude-artisan`, `guildhall fake-artisan`, `guildhall fake-director` and `guildhall artisan start`, with or without arguments (deny takes precedence over the broad `guildhall` allow); task orchestration and `artisan stop/status/resume` remain allowed.
  It denies any `git` command with `--output` (which writes a file), denies edits to the copy's `config.toml` and `managed.toml`, and registers a `UserPromptSubmit` hook that runs [`guildhall director turn-started`](#the-end-of-a-turn) at the start of every turn and `Stop` and `StopFailure` hooks that run `guildhall director turn-ended` at the end of every turn;
- `startup.md`: the startup instructions, saying the session is starting up and recovering, then, when there are any, the repositories' [policies](configuration.md#repository-policies) under `## Repository policies`, one line `- <repo>: <policy>` each, or `- <repo>: policies cannot be read (<reason>)` for a refused file, from each default branch as fetched just before the session renders, a snapshot that a merge mid-session changes in `director status` at the next advance and here at the next reload, then, when there are any, every live [note](#notes) under `## Notes`, grouped by kind as `director note list` prints them and opening with `Notes total <N> KB, over 8 KB: compact them (merge, shorten or remove).` when their text totals more than 8 KB, then the in-flight snapshot, each `needs_me` item of the [status feed](#status) with its command, except a `checks` item, which says the session supervisor advances it ([checks](#checks)), then, when there are any, the feed's `running` attempts under `## Running`, which the Director confirms before `director ready`, one line each: `running: attempt <n> of <step> of <repo>/<task>; state <state>; heartbeat <age> ago; last output <age> ago; <reason>; check with <command>`, with `no heartbeat` for an attempt that has none and its ages by the supervisor's clock, and then, when there are any, the feed's `parked` attempts in a section of their own, as information rather than work, one line each: `parked: attempt <n> of <step> of <repo>/<task> <outcome>; reason: <reason>; retry with <command>`, ending ` until <repo>/<task>, …` for a park held until tasks merge, and then the feed's `held` items under `## Held`, one line each: `held: <decision <id> of <repo>/<task> | follow-up <id>> until <tasks> (waiting for <tasks>): <reason or text>; lift with <command>` ([holds](#holds));
- `manifest.json`: the digest of every file above.

It binds the session's [endpoint](#the-end-of-a-turn) and then runs the runtime's session command as its child in that directory (for Claude, in the stable workspace described below), inside a containment unit of its own ([runtime containment](decisions/0084-director-stop-admission.md)), the same backend as an [Artisan's](artisan.md) (a Windows Job Object, a Linux cgroup or a process group with the supervisor as child subreaper, a macOS process group), so every process the runtime starts, such as its shell and tool children, is the session's, except the launchers of the attempts it starts, which leave the unit under their own [ownership](artisan.md) and [stop](#stop), with the terminal's input and output and the first message naming `startup.md`.
The session directory always lies under the copy, so the session's own `guildhall` commands find the copy by [copy discovery](running.md#copies); no environment variable names it.
For Claude Code that is `claude --append-system-prompt-file <session-directory>/role.md --add-dir <copy> --settings <copy>/director/claude/.claude/settings.json --plugin-dir <copy>/director/claude/.claude/plugins/lanterns --plugin-dir <copy>/director/claude/.claude/plugins/clerk [--model <model>] [--effort <level>] -- "<first message>"`; see [runtime settings](adapters.md#runtime-settings) for effort resolution and preflight refusals.
Claude runs in **`<copy>/director/claude`**, a stable workspace reused by starts, reloads and handoffs, with the first message naming the numbered session's `startup.md` by absolute path.
That first message is `/version startup "<absolute startup.md path>"`: the session's `/version` command, `.claude/commands/version.md`, runs `guildhall version --startup`, the model replies with only its line and any warning, then reads `startup.md` and recovers as before, all in that one turn ([version](contracts/cli.md#version)).
The line names the `guildhall` binary on `PATH`: its short revision, the pull request and task it ends at, its commit date and how far it is behind main; the warning says when it is behind main, was built modified, or is not the binary this session's supervisor recorded, by sha256, as after installing a new binary without reloading, and when the supervisor refused to move onto a new binary at the reload that started the session ([the supervisor's binary](#the-supervisors-binary)).
Typing `/version` in the session shows the fuller form, and `/version startup` the same line again.
The workspace also holds the embedded [Lanterns](lanterns.md) plugin at `.claude/plugins/lanterns/` and the [Clerk](clerk.md) plugin at `.claude/plugins/clerk/`, both rewritten from the running binary at every start, so the session also has Clerk's read-only `/guild`, `/decisions`, `/task`, `/inspect` and `/queue`.
A Director's very first start shows Claude's native folder-trust dialog: review the generated settings, then choose **Yes, I trust this folder** once.
Choosing **No, exit** ends the runtime; Guildhall reports its nonzero exit status.
After approval, later starts, reloads and handoffs keep the same folder identity and do not show the folder-trust prompt again.
Moving the copy or clearing Claude's saved trust may require approval again.
The supervisor refreshes the workspace's generated `.claude` tree from the numbered record before each launch, after the preceding runtime has ended; do not edit this generated tree.
The numbered session records, manifests, prompts, notes and endpoints remain unchanged in purpose.
The allow/deny rules are unchanged, ordinary permission prompts remain enabled, and Guildhall never writes Claude's own trust state or uses a permission-bypass flag ([decision](decisions/director-stable-claude-workspace.md)).
The terminal's interrupt reaches the runtime, which handles it; the supervisor waits for the runtime to end, records the session `stopped`, and exits 0, or 1 naming the status when the runtime exited nonzero.
On Unix a hangup or termination of the supervisor, such as the terminal closing, is passed on to the runtime's unit as a termination; the supervisor waits for it to end, records the session `stopped`, and exits 1 naming the signal.
A signal the supervisor handles that arrives after the session is recorded but before the runtime starts starts no runtime: the session is recorded `stopped` and the supervisor exits 1 naming the signal.
A session already stopped or superseded by a newer one is left as it is.
While the runtime runs, the supervisor reads its session about once a second; once [`director stop`](#stop) has recorded it `stopping`, it ends the runtime at the [end of the turn](#the-end-of-a-turn), records the session `stopped` and exits 0.
On the same tick it runs the [checks watcher](#checks) and the [dependency watcher](#dependencies).
When it ends the runtime for a [reload](#reload) it records the session `stopped` with the end reason `reloaded`, reads the copy's config again and starts session `<n+1>` the same way in the same terminal, which records the reload `applied` from session `<n>` to `<n+1>`.
The config read again chooses the runtime too: unless `director start` named one, a changed `[director] adapter` takes effect, checked on `PATH` as the preflight checks it, and a runtime refused then ends the supervisor with exit 1, with session `<n>` recorded `stopped` as `reloaded` and the reload left requested.
A `director stop` recorded while the runtime is ending for the reload wins: the session is recorded `stopped` as for a stop and the supervisor exits 0 with no new session.
On Unix, once session `<n>` is recorded `stopped` as `reloaded` and no stop won, the supervisor moves onto a new `guildhall` binary if one is on `PATH`; see [the supervisor's binary](#the-supervisors-binary).
A runtime that ends by itself is recorded `stopped` and gives the terminal back, even with a reload requested; that reload, like one requested while no supervisor runs, is recorded `applied` by the next `director start`.
However the runtime ends, the supervisor kills whatever it left running in its unit and records the session `stopped` only once the unit is empty; a unit not proven empty within 10 seconds of the runtime's end makes the exit 1, naming the survivors and leaving the session stopping and sweeping for recovery with `guildhall director stop`.
On Linux the runtime is also killed if the supervisor dies.

### The runtime's record

Once the unit reports the gated or suspended runtime process created, before its program can execute, the supervisor records the unit on the session, as an Artisan's [ownership](artisan.md) records its worker: its kind, its name (the Job Object's name or the cgroup's directory; a process group has none), the Linux detection reason, and the runtime's process identity ([decision](decisions/0090-director-runtime-record.md)).
A record that fails ends the runtime and its unit, and the supervisor exits 1 naming the failure, so no runtime runs that the record does not name.
The record is what lets the session's processes be found after the supervisor has died, such as when its terminal is closed with `kill -9` or a crash: [`director stop`](#stop) sweeps the recorded unit and `director start` refuses while it holds a process.
The [record-before-run decision](decisions/0090-director-runtime-record.md) describes the Unix gate and Windows suspension that enforce this guarantee.

## Skills

The release ships the Director's own skills beside the stage skills, each rendered into every session ([decision](decisions/0075-director-skills.md)):

- `guildhall-director-startup`: handle `startup.md`, work each in-flight item, then `director ready`.
- `guildhall-director-inbox`: on every wake line and turn start, read `director status` and each unread message, act and acknowledge.
- `guildhall-director-tasks`: turn what the Guildmaster says into commands, from giving work and answering decisions to merging, retrying, stopping, rulings and pausing.
- `guildhall-director-stage`: run a [Director stage](interpreter.md#director-stages) from its brief and finish it with `step finish`.
- `guildhall-handoff`: run and record the note audit, then reload; the Guildmaster can type `/guildhall-handoff`.
- `guildhall-maintenance`: when a [maintenance](#maintenance) item is due and at every handoff, audit the notes against the store and record the audit, and launch a due hallmark.
- `guildhall-self-review`: after a task merges, when the [quality settings](configuration.md#quality-settings) turn it on, record the task against the [quality rubric](quality.md) without changing it.

Between them they name every command of the [README's list](../README.md#the-commands) the Guildmaster would otherwise type ([D57](decisions/0057-director-conversation-first.md)).

## Chat address

The baseline Director role opens a [task grant](tasks.md#task-grants) only when the Guildmaster asks for one, never on its own judgement or from an Artisan's request, and answers its `grant-<id>` decision `approve` only with the Guildmaster's words.
That is conduct, not an enforced boundary: the session may run `decide answer`, as it does for every decision it records for the Guildmaster ([decision](decisions/task-grants.md)).

The baseline Director role addresses the user plainly as "Guildmaster" at least once in every chat reply, including bad news.
That address is chat-only, never added to commits, PR text, task records, documents or messages.
Optional light guild flavour must not obscure technical content.

Every chat reply of the baseline role has one fixed layout, so the newest reply always shows what needs the Guildmaster without scrolling back:

1. The marker line: the [Lanterns](lanterns.md#reply-lights-and-the-status-line) marker first, with nothing before it, then a headline that counts the asks, such as `🟡 Guildmaster, two things need you.`
   The marker agrees with Needs you: 🔴 when something failed, is stuck or blocks work, 🟡 when an ask is open, 🟢 when nothing needs the Guildmaster.
2. **Needs you**: one list item per open ask (a decision, an open grill round or a Director question), `<id> · <what> · <record>` such as `d96 · approve the plan · accept-1`, in the `needs_me` order of `director status --json`, which `/clerk:guild` also uses; with no asks, the single line `Nothing needs you.`
   An ask's full question and options are written once, in the reply where it opens, and later replies carry only its line; `/clerk:decisions` shows a decision in full.
3. **What I did**: plain sentences on what the Director did and what happens next, only when it acted on the Guildmaster's words in that turn.
   A grill round takes this section's place, after its own line at the top of Needs you, and has no Routine box.
4. The Routine box, last: a fenced `text` block with one line per task whose state changed since the previous reply, `<id> · <state> · <PR or "no PR">`, in the status's `tasks` order, ending with `<N> unchanged · /clerk:guild` and `as of #<as_of.sequence>`; it is left out when nothing changed.

Routine wakes stay quiet, so fewer messages push the asks out of sight.
A `published`, `merge-queued`, `approval-carried`, `rebased` or `rebase-escalated` message, and checks pending or passing, get no reply of their own and show in the next Routine box.
The Director replies unprompted only when something needs the Guildmaster, a task merges, something fails or something is stuck.
With function hooks on, Lanterns [draws rules](lanterns.md#reply-lights-and-the-status-line) before What I did and the Routine box and a border around the box; without them the same text reads as plain markdown.

A custom role, from `[director] role` or a copy's own `baseline/roles/director.md`, does not get this layout and is responsible for its own conversation instructions.

## Artisan workspaces

The Director's `herdr_session` also selects [workflow Artisan workspaces](artisan.md#workflow-artisan-workspaces), independently of whether the Director itself runs with `--tab` or in a terminal.
That page owns their child labels, best-effort placement under the Director, lifecycle and recovery rules; [Herdr access](adapters.md#herdr-access) describes the session prerequisites.

## The Director's tab

```sh
guildhall director start [runtime] --tab [--check] [--copy <dir>]
```

With `--tab`, `director start` is the wrapper the Guildmaster runs: it opens the Director's tab in Herdr and runs the session supervisor there, so the Director has its own visible tab (R1.37) that firstmate can reach remotely ([decision](decisions/0078-director-tab.md)).

- It runs the same preflight, then a `herdr` check: the config's [`herdr_session`](configuration.md#settings) setting names the Herdr session, and `herdr status` must report that session's server running, Herdr 0.9.5 or newer ([Herdr access](adapters.md#herdr-access)).
  With no `herdr_session` the check is refused, naming the setting and running without `--tab`; `--check` exits 0 after it.
- A live session is refused, `director: live`, before any tab opens.
- It finds the workspace labelled with the Director's `name` in that session, or creates one without focus, and creates a tab labelled `director <name>` in it, without focus, in the copy directory.
- Once the tab's shell is at its prompt, it types `guildhall director start [runtime] --copy <dir> --herdr-session <s> --herdr-workspace <w> --herdr-tab <t> --herdr-pane <p>`, with the running executable's absolute path, quoted for the pane's shell as [`herdr.ShellCommand`](contracts/herdr.md) quotes it (POSIX shells and PowerShell; any other shell is refused with nothing typed).
- It waits until a new session record carries that pane, prints a `tab:` line naming it and then the session as one JSON line, and exits 0.
  If the typed command ends first, as a refused preflight in the tab would, or no such session appears within two minutes, it exits 1 naming the tab.

Normal operation leaves one Director tab in the workspace ([decision](decisions/director-tab-replacement.md)).
When Guildhall creates the workspace, it replaces Herdr's starting shell under the [adapter's starting-shell cleanup contract](contracts/adapter.md#contract); a cleanup failure aborts the launch before typing input and names both tabs.
After a successful start, it retires earlier Director tabs only when a persisted session binding proves ownership, the session is stopped or superseded, its runtime unit is empty, and the tab contains one idle shell pane.
A session's [adopted](#start) pane is never retired, since Guildhall did not create its tab.
A failed runtime leaves its output visible until the next successful start replaces its tab; a command that fails before recording a session leaves an unrecorded tab that Guildhall cannot safely reclaim.
Busy, split, foreign or ambiguous older tabs are left untouched; failures while retiring them are reported on stderr without failing the new session.
Closing tabs is presentation cleanup only and never changes a task or session record.
A reload, including `director reload --now` at a handoff, starts the next runtime under the same supervisor in the same tab and pane, without opening another tab.
Guildhall never closes the session's own starting workspace or shells in an existing workspace whose identities it did not record, including shells left by older versions.
Those unrecorded shells need manual cleanup.

CI runs the wrapper over a terminal double whose panes run each typed line with `sh` on Linux and macOS, and over an in-process double on every platform; the [recorded Herdr responses](contracts/adapter.md) prove the Herdr client's side.
The acceptance is this lab checklist, not CI, in an isolated named Herdr session:

- zsh on WSL, Git Bash on native Windows, and PowerShell 5.1 on native Windows: `director start --tab` creates the workspace labelled with the Director's name once and reuses it, opens the `director <name>` tab without taking focus, the typed line starts the supervisor, and the printed session carries that tab's pane;
- a second `director start --tab` while that session runs is refused with `director: live`;
- after the supervisor is killed and its runtime is proven ended, `director start --tab` supersedes the old session and replaces its idle tab;
- first start, a start after a failed or stopped runtime, and plain reload or `reload --now` each leave exactly one Director tab;
- a message to the Director types its wake line into the new tab.

The tab lifecycle has been checked in a guarded named Herdr lab with the fake runtime; cross-shell acceptance above remains separate, and macOS stays CI-only (R1.30).

## Ready

```sh
guildhall director ready [--copy <dir> | --store <file>]
```

`director ready` moves the newest session from `recovering` to `running`, which records its ready time, and prints the session as one JSON line.
A `running` session is printed unchanged, exit 0; no session, or one that is `paused`, `stopping` or `stopped`, is exit 1.

While the newest session is `recovering` it starts no new work: `task new`, and `task start` of a task with no attempt yet, are refused with exit 1 naming `guildhall director ready`, and record nothing.
Everything else is unaffected, so the Director recovers what is in flight first: `task start` of a task already under way, `step done`, `step retry`, `decide answer`, `messages` and every read.

## Pause and resume

```sh
guildhall director pause [--copy <dir> | --store <file>]
guildhall director resume [--copy <dir> | --store <file> --state <dir>]
```

`director pause` moves the newest session from `running` to `paused` and prints it as one JSON line ([decision](decisions/0073-director-pause-stop.md)).
A paused session starts no new attempt, while attempts already running finish.
The hold is read in the same store transaction that records a new attempt, so an advance that started before the pause and records its attempt after it is held too:

- `task start` and `step retry` start no attempt: they print `{"wait":"paused","step":"<step>"}`, the step whose attempt was held, which the task's `next.json` records, and [`needs_me`](#status) lists the task as `paused`, so a task the Guildmaster asked for during a pause is listed after the resume.
- `task start --decision` still feeds the Guildmaster's answer, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `step done` still takes the attempt's result and opens any decision it leads to, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `decide answer`, `messages`, `artisan stop` and every read are unaffected.
- The [start queue](#the-start-queue) is kept, but nothing drains while the session is held: a held start is never queued, and the supervisor's start-queue watcher starts nothing until the resume.

`director resume` moves a paused session back to `running` and prints one JSON line, `{"session":{...},"needs_me":[...]}`, with the [status feed](#status)'s `needs_me`, which the Director then works using each item's recovery command.
It is the Director's resume, not [`artisan resume`](artisan.md#commands), which settles one attempt whose launcher is gone.

A resume that moves the session wakes the Director once for what waited while it was paused, such as messages queued `paused` and held tasks: when `needs_me` holds anything for the Director, anything but `checks`, `dependencies`, `merge-queue`, `decision`, `install` and `maintenance` items, and the session has a bound pane (a [parked](running.md#step-park-and-step-unpark) attempt is not in `needs_me`, so it wakes nothing), it wakes it with one line of counts by kind, never any text, as a [wake](messaging.md#waking-the-director) does, handed to a Claude Code session's waiter and typed only into the fake runtime's pane, and prints `"wake":{"decision":"woke"}` in its line:

```text
guildhall: resumed, 2 waiting (messages 1, paused 1)
```

Otherwise it delivers nothing and prints `"wake":{"decision":"queued","reason":"<reason>"}`, `nothing waiting` or one of the [wake](messaging.md#waking-the-director)'s session, waiter and pane reasons, and the work waits for the Director's next turn.
If reading the session for the wake fails, resume reports the error on standard error and omits `wake`, without failing or undoing the resume.
The resume records no wake: each message keeps the `queued` wake it recorded while the session was paused ([decision](decisions/0086-director-checks-watch.md)).
A `checks`, `dependencies` or `merge-queue` item is the supervisor's and a `decision` or `install` item the Guildmaster's, so none counts or wakes.

A paused session is printed unchanged by `pause`, and a running one by `resume`, exit 0, with no `wake` and nothing typed.
Both are exit 1 for no session, a session that is `recovering` (run `director ready`), `stopping` or `stopped`, and a session whose supervisor is gone, naming `director stop`.

## Checks

A task whose validate step waits for its published head's checks, `next.json` `{"wait":"checks","step":"validate"}`, is moved on by the session supervisor, not by the Director ([decision](decisions/0086-director-checks-watch.md)).
On its first supervisor tick, then on the first tick at least [`checks_refresh`](configuration.md#settings) after the previous round ends, the supervisor's checks watcher lists the open tasks waiting for checks and advances each in turn as `task start` does, in the supervisor process and with no model turn, while the session is `recovering` or `running`.
The interval has a 30-second minimum, including when `checks_refresh` is `"0s"`.
The advance reads the checks under the [validate step's pacing](interpreter.md#validate): checks still pending send nothing new, since a head's `checks-pending` message is sent once, and checks that finish send `checks-passed` or `checks-failed` and take the task on, which [wakes](messaging.md#waking-the-director) the Director as any send does.
A repository with [`no_ci`](configuration.md#repositories) passes at its first read, so only a repository with CI waits here.
While the session is `paused` or `stopping` the watcher advances nothing.
One round runs at a time; a failed advance, such as an unreachable forge, is appended with its standard-error output to `checks.log` in the session's directory and tried again a refresh later, and the supervisor lets a running round end before it records the session stopped.
With no supervisor running, the task waits until someone runs the `task start` its `checks` item in [`needs_me`](#status) names.

A pull request that is behind or conflicting does not wait for the Director: validate rebases a conflicting one and reads a behind one's checks as usual, and the [merge queue](#merge-queue) rebases its front ([tiered rebase](interpreter.md#tiered-rebase)).
Only a front that rebased three times in one stay (`rebase-stuck`), or a copy that cannot rebase, such as one whose forge gives no base tip, records its mergeability and sends a `pr-blocked` or `rebase-stuck` message once per head and state.
The report gives the URL, head commit, base tip (the base branch's tip when measured), conflicting paths, behind count and options, and the task waits for the Director, so the checks watcher does not keep polling it; the Director chooses the response.
For such a task, [`step send-back`](step-send-back.md) can start a new attempt of a succeeded Artisan step with Director feedback, followed by normal rebase-aware review and republishing to the same PR, and [`task hold-merge`](tasks.md#holding-a-merge) releases a stuck front.
Holding the task, leaving the work unadvanced, using the existing attempt stop or Director-stage abandonment paths, and rechecking after an external change are alternatives, not a prescribed recovery sequence.
See the shipped `guildhall-director-tasks` skill for the commands and their limits.

## Artisan Herdr workspace cleanup

With [`herdr_session`](configuration.md#settings) set, the session supervisor also closes finished Artisans' Herdr workspaces with the [workspace close sweep](artisan.md#workflow-artisan-workspaces) ([decision](decisions/artisan-workspace-close-sweep.md)).
It runs one sweep when each session starts, a reload's included, so a Herdr workspace that became due while no supervisor ran is closed, then one on the first tick at least 5 minutes after the previous sweep started.
A sweep runs in the supervisor process, one at a time and off the decision path, so a slow Herdr never delays a stop or reload; the supervisor lets a running sweep end before it records the session stopped.
Each attempt it leaves open is reported once with an unread `herdr-workspace-left-open` message that does not wake the Director; an unreachable Herdr writes one standard-error line and the next sweep tries again.
`director start --tab`, the wrapper, does not sweep; the supervisor in its tab does.

## Dependencies

A task the [dependency gate](interpreter.md#the-dependency-gate) holds, `next.json` `{"wait":"dependencies","step":"<step>"}`, is moved on by the session supervisor too.
The supervisor runs a second watcher of the same kind as the [checks watcher](#checks), on the same interval and minimum, which lists the open tasks whose `next.json` waits for dependencies and advances each in turn as `task start` does, while the session is `recovering` or `running`.
When a dependency merges, its dependents' next round passes the gate and starts research with no model turn (R1.14); while a dependency is still open the advance sends nothing new, since a `dependency-held` message is sent once per state.
Nothing is advanced from inside the dependency's own advance, which would take a second task lock inside the first.
A failed advance is appended to `dependencies.log` in the session's directory and tried again the next round.
With no supervisor running, the task waits until someone runs the `task start` its `dependencies` item in [`needs_me`](#status) names, or starts it anyway with [`task start --ignore-deps --reason <text>`](running.md#task-start).

## Merge queue

A task whose merge is authorized and waits in its repository's [merge queue](interpreter.md#the-merge-queue), `next.json` `{"wait":"merge-queue","step":"deliver"}`, is moved on by the session supervisor as well.
A third watcher of the same kind as the [checks watcher](#checks), on the same interval and minimum, lists the open tasks whose `next.json` waits on the merge queue, then each repository's front whose merge is [held](tasks.md#holding-a-merge), whatever its `next.json` waits on, each task once, and advances each in turn as `task start` does, while the session is `recovering` or `running`.
A held front's advance releases the front, so the next task claims it in the same or the next round.
The advance claims the front for the first task in queue order, and the front then rebases, pushes, waits for CI and merges with no model turn; the others send nothing new, since `merge-queued` is sent once per place.
A paused or stopping Director pauses the queue: the watcher advances nothing, and nothing else advances a queued task but `task start`.
The order is not the watcher's: the front is chosen in one store mutation from priority, ready time and task id, so the order in which the watcher advances tasks does not matter.
A failed advance is appended to `merge-queue.log` in the session's directory and tried again the next round.
[`needs_me`](#status) lists such a task as a `merge-queue` item, after the `dependencies` ones; like them it is the supervisor's, so it neither counts nor wakes the Director on resume.
The Director hears of a queued task through `merge-queued`, `rebased`, `rebase-escalated`, `approval-carried`, `merged` and, for a front that rebased three times, `rebase-stuck`.
[`task priority`](tasks.md#priority) reorders the tasks behind the front, and [`task hold-merge`](tasks.md#holding-a-merge) takes a task out of turn without cancelling it.

## Stop

```sh
guildhall director stop [--now] [--copy <dir> | --store <file> --state <dir>]
```

`director stop` ends the Director session and its Artisan attempts ([decision](decisions/0073-director-pause-stop.md)).
It records the newest session `stopping` and `sweeping`, then stops every running attempt in the store, records the sweep finished, and prints the session as one JSON line.
A stopping session starts no new attempt, as a paused one does, and neither does a session whose stop is still `sweeping`, even once its supervisor has recorded it `stopped`, so every attempt recorded before the stop is one the stop sees.
Once the sweep is finished the hold lifts, and a `task start` by the Guildmaster with no Director running works again; until then [`director start`](#start) is refused, so no new session lifts the hold either.
An attempt with an owning launcher is stopped as [`artisan stop`](artisan.md#commands) does.
An attempt nothing owns, a [Director stage](interpreter.md#director-stages) such as the grill or an Artisan attempt an advance recorded but has not launched, is recorded `stopped` under its task's lock, after any advance or `step finish` in progress on the task, so it no longer counts against the [attempt caps](#guard-rails), [`needs_me`](#status) lists it to retry, a later `step finish` of it is refused and no launcher can take it.
A Director stage whose result `step finish` already published has finished its work and is left for its advance to take.
The session's supervisor then ends the runtime and records the session `stopped`: at the [end of the current turn](#the-end-of-a-turn), or, with `--now`, at once.
A stopping session is stopped again without changing it, and `stop --now` after a plain stop ends the runtime at once.
Each attempt that cannot be proven stopped is reported on standard error and makes the exit 1; the session stays `sweeping`, holding new attempts, and running `stop` again, even over a session since recorded `stopped`, finishes the sweep.

When a session's supervisor is gone, `stop` takes over cleanup: it records the session `stopping`, ends every process still in the session's [recorded runtime unit](#the-runtimes-record), killing them from outside as a resumed Artisan attempt's unit is swept, stops its attempts and then records it `stopped` with the end reason `superseded`.
Survivors of the unit that are not proven ended within 10 seconds, or a unit that cannot be read, are reported on standard error and make the exit 1, with the session left `stopping` and `sweeping` and not recorded `stopped`; the diagnostic tells the user to run `guildhall director stop` again to finish the sweep.
A session from a release without runtime records has no unit to sweep.
In current releases, a crash before the record cannot leave an executing runtime.
A previously `stopped` session whose recorded runtime unit is not proven empty remains eligible for `stop` recovery.
Before either sweep, the stop request sets `sweeping` atomically while preserving the stopped state and end metadata; it holds admission on failure until a later `director stop` completes both sweeps.
No session, or a session already `stopped` whose stop has finished its sweep and whose runtime unit is proven empty, is exit 1.

The stop does not yet send the Director a `stop-requested` message asking it to bring its [notes](#notes) up to date first; that lands later, and notes kept as the Director goes cover it meanwhile.

## Handoff

A handoff is a fresh session picking up from this one: the Director runs and records the [note audit](#maintenance), then runs [`director reload --now`](#reload) ([decision](decisions/0094-director-notes.md)).
The single handoff-note document and `director handoff --note-file` that recorded it ([D82](decisions/0082-director-handoff-reload.md)) are retired: stores keep the [handoff notes](store.md#handoff-notes) already recorded and `store doctor` still decodes them, but nothing writes or shows them.

## Notes

```sh
guildhall director note add --kind promised|guildmaster|plan [--task <repo>/<id>] <text> [--copy <dir> | --store <file>]
guildhall director note edit <id> <text> [--copy <dir> | --store <file>]
guildhall director note remove <id> [--reason <text>] [--copy <dir> | --store <file>]
guildhall director note list [--copy <dir> | --store <file>]
```

A note is one small record the Director keeps of something no other record holds: a promise to the Guildmaster (`promised`), a preference or context the Guildmaster gave (`guildmaster`), or Director intent (`plan`), optionally about one task.
A standing rule for one repository is not a note: it belongs in that repository's [`.guildhall/policies.md`](configuration.md#repository-policies), so the Director proposes the change as a task or tells the Guildmaster the exact line, and keeps `guildmaster` notes for context that is not a policy.
Its text is one line of at most 1024 bytes; an edit replaces it, and a remove, with an optional one-line reason of at most 256 bytes, takes the note out of the live notes while the store keeps every version ([store](store.md#director-notes)).
Only the newest session that is not `stopped` writes notes; any other is exit 1 and records nothing, as is a note naming a task that does not exist.
To keep tokens low, `add`, `edit` and `remove` print only a receipt such as `n7 added`, and making the same change again prints the same receipt.
`note list` prints the live notes grouped by kind, each kind under a `### <kind>` line, one line per note, such as `- n7 guildhall/d20 (s16, 2d): rebase after d18 merges`: the id, the task if any, the session that last changed it and its age, then the text, with `[task done, check it]` at the end when the task is `done` or `cancelled`; notes are never removed automatically.
With no live notes it prints `No notes.`

Notes reach only Director sessions, never an Artisan.
Requirements, constraints or clarifications the Guildmaster gives about one task go in that task's [task context](tasks.md#task-context), `guildhall task context add`, which every later stage of the task is given, not in a `guildmaster` note; a `guildmaster` note keeps a preference that is not about one task.
When the Guildmaster wants a task's Artisans to see another task's document, such as the spec its title names, link it with `guildhall task link add --document <repo>/<task>/<name>`, quoting their words, so every later stage is given a verified copy ([task links](tasks.md#task-links)); never paste the document into task context.
When the Guildmaster asks for one task to run with its own value of a task-scoped setting, such as `default_effort`, record it with `guildhall task override set --key <key> --value <value>`, quoting their words ([task overrides](tasks.md#task-overrides)); never set one on your own judgement, and never write it as a note.

Every new session's `startup.md` lists the live notes in the same form before the in-flight snapshot, so the Director writes a note in the same turn something arises that no record holds, rather than at the end of a session.
When their text totals more than 8 KB the list opens with a line asking the Director to compact them; it is a warning only, and no note is ever refused for the total.
Notes are not in the [status feed](#status), which `note list` covers.
A note saying "once X merges, do Y" is better kept as a [hold](#holds), which comes back by itself.
The Director reviews every note regularly in the [note audit](#maintenance), removing what the store already holds and proposing a task or procedure for a recurring kind of note.

## Maintenance

```sh
guildhall director maintenance record --notes --summary <text> [--copy <dir> | --store <file>]
guildhall director maintenance list [--json] [--copy <dir> | --store <file>]
```

Director maintenance has two parts, which the [`guildhall-maintenance`](#skills) skill runs and Guildhall says are due, so neither depends on the Director remembering ([decision](decisions/director-maintenance.md)):

- The note audit: for each live [note](#notes), is it still true, does the store already hold it (a task, decision, message, document or config), and is it a recurring kind of note that should become standard procedure or a follow-up task?
  The Director removes, edits and compacts notes on its own, each removal with a reason naming the record that holds it, and brings proposed tasks and procedure changes to the Guildmaster as a short numbered list, creating nothing from it before they approve.
  Every [handoff](#handoff) runs it too.
- The hallmark: for each repository whose hallmark is due, the Director launches a [`hallmark` task](tasks.md#task-kinds) with `guildhall task new --kind hallmark` and `guildhall task start`, and reads the self-review ledger; there is nothing to record, since Guildhall reads the hallmark from its task ([decision](decisions/engine-owned-hallmark-cadence.md)).
  The task runs one read-only stage that uses `guildhall-hallmark`, is given the repository's previous hallmark report, and finishes as done with a `finished` message naming its report and no pull request, so it sends no `merged` message, and opens a `follow-ups` decision listing the report's ranked follow-ups ([decision](decisions/report-only-workflows.md#the-guildmaster-picks-the-follow-ups)).
  The Director reads the report, presents the `follow-ups` decision to the Guildmaster, answers it with `guildhall decide answer` in their words (`pick` and the chosen ids, or `none`), and runs `guildhall task new` for each picked id only; Guildhall creates no task from the answer.
  It needs `task new`, so maintenance waits until the session is no longer `recovering`.

Two [`[director]` settings](configuration.md#the-director-table) set when each part is due, and 0 turns a part off:

| Setting | Default | Due when |
|---|---|---|
| `note_audit_every_sessions` | 3 | the newest session's number less the session of the last recorded note audit, or the baseline session if none, is at least the setting |
| `hallmark_after_merges` | 4 | per repository, the `merged` messages sent after the launch of the repository's last hallmark, or after the baseline if none or if that is later, are at least the setting, and no hallmark task of the repository is open |

The settings are recorded on each session when it starts, so a change takes effect at the next session; the feed reads the newest session's.
Counting starts at the baseline: the start of the earliest session recorded with a maintenance policy, which in an upgraded copy is the first session started by a release with maintenance, and in a fresh store session 1.
Sessions and merges before it count for nothing, so an upgrade shows no item at once: the first hallmark is due after `hallmark_after_merges` merges made after it, and the first note audit `note_audit_every_sessions` sessions after it.
A session started with both parts off records no policy, so it is not a baseline.
A merge counts after a note audit, a hallmark or the baseline when its message's send receipt is newer than the run's receipt, the hallmark task's create receipt or the baseline session's start receipt.

A repository's last hallmark is its newest done task, by when it closed, whose bound workflow has a `hallmark` step and that recorded its `finished` document and a report: the same task whose report the next hallmark is given as `previous` (`interp.LastHallmark`).
A cancelled or failed hallmark is never the last hallmark, so it never resets the count.
The count starts at the hallmark's launch, its task's create receipt, so merges made while it runs count toward the next one.
While a hallmark task of the repository is open the hallmark is not due, and status names that task as `running_task`; once it is cancelled the item comes back.
A repository with no done hallmark task counts instead from its newest hallmark run, recorded by `maintenance record --hallmark` before that was retired.
Status resolves each task's workflow with the repository's settings in the config; with no config, or one that cannot be read, it matches the task's workflow revision against the shipped workflows only, so a hallmark of a Director-local workflow or of one with workflow overrides is passed over and the repository counts from its runs.

A due part is a `maintenance` item in [`needs_me`](#status), computed on each status read with nothing recorded: `note audit due: <n> sessions since the last (session <s>)`, `since the upgrade` before any audit, with the command `guildhall director maintenance record --notes --summary <text>`, and one per repository `hallmark due: <n> merged since the last`, with that `repository`, the `count` of merges and `guildhall task new --repo <r> --kind hallmark --title <text> <id>`.
It is not urgent: it adds nothing to the wake line, does not wake the Director on [resume](#pause-and-resume) and does not hold back [`director ready`](#ready); the Director works it after recovery, and `startup.md`'s in-flight list shows it like any other item.

`maintenance record --notes` records a note audit as the newest session, which must not be `stopped`, as notes are; `--summary` is one line of at most 1024 bytes, what was removed and what was proposed, such as `removed 4 (held by d47, D94, config); proposed 2: d58, skill change`.
It prints `run <k> recorded`; recording the newest run again prints the same receipt and records nothing.
`--hallmark` is a usage error, `hallmarks are recorded when the task finishes`, as are a missing `--notes`, `--repo` or `--task` with `--notes`, and a missing summary; a summary that is blank, multi-line or too long, no session or a session that cannot write is exit 1 and records nothing ([store](store.md#director-maintenance)).
The note audit item leaves `needs_me` once its run is recorded, and a hallmark item once its hallmark task is created.
`maintenance list` prints every recorded run, oldest first, one line each, `<k> s<session> <time> notes: <summary>`, or `<k> s<session> <time> hallmark <repo>/<task>: <summary>` for a hallmark run recorded before that was retired, or `No maintenance runs.`; `--json` prints the runs as a JSON array.

## Holds

A hold is an item the Director keeps out of the way until named tasks merge, so "once X merges, do Y" needs no note ([decision](decisions/0096-hold-until-merged.md)).
There are three kinds: a parked attempt with [`step park --until-merged`](running.md#step-park-and-step-unpark), an open decision deferred with [`decide defer`](decisions-cli.md#deferring-a-decision), and a follow-up with no attempt or decision yet:

```sh
guildhall director follow-up add --until-merged <repo>/<task>... <text> [--copy <dir> | --store <file>]
guildhall director follow-up list [--copy <dir> | --store <file>]
guildhall director follow-up done <id> [--copy <dir> | --store <file>]
```

Every hold names one or more tasks of this store with `--until-merged <repo>/<task>`, repeated for several.
Each must be open when the hold is recorded and must not be the held item's own task; a task already done is refused with `<repo>/<task> already merged; act now instead`, as is one already cancelled.
A hold is released once every task it names is `done`, or at once when any of them is `cancelled`, marked `dependency cancelled`.
Release is derived: [`director status`](#status) judges each hold from the task states it reads, so it shows with no supervisor running and needs no write; what released it is named from the task's state receipt, as `released: <repo>/<task> merged <time>` or `dependency cancelled: <repo>/<task>`.
Nothing is retried, answered or created on release: the item comes back in `needs_me` with its reason and what released it, and the Director acts.

A follow-up's text is one line of at most 1024 bytes, as a note's is.
Only the newest session that is not `stopped` writes follow-ups; `add` prints `f<k> added` and `done` `f<k> done`, and repeating either prints the same receipt.
`follow-up list` prints the follow-ups not done, one line each, such as `- f3 until guildhall/d45 (waiting for guildhall/d45): rebase d20`, with `[released: …]` in place of the wait once released, or `No follow-ups.`
A follow-up stays, released, until `follow-up done`.

While the session is `recovering` or `running`, the session supervisor runs a fifth watcher of the same kind as the [checks watcher](#checks), on the same interval and minimum, which reads status each round and, for each released hold that still matters, sends one Director message of type `hold-released` from `supervisor` and wakes the Director for it.
A hold still matters while its park would otherwise count (task open, attempt the step's latest and failed or stopped), its decision is open, or its follow-up is not done.
The message id is `hold-released.<kind>.<repo>.<task>.<item>.<cycle>`, the item being a park's `<step>.<n>` or the decision id, and `hold-released.follow-up.<id>` for a follow-up, which is filed under the first task it named; a release seen again by a later round is not sent again, so each release wakes the Director once.
The dependency's own `merged` message already wakes the Director when it merges, so the watcher's delay of up to one `checks_refresh` only adds the reminder; its failures go to `holds.log` in the session's directory.

Every new session's `startup.md` lists a held park's `## Parked` line with ` until <repo>/<task>, …` at its end, and a `## Held` section with one line per deferred decision and follow-up still waiting, with the tasks it waits for and the command that lifts it.

## Reload

```sh
guildhall director reload [--now] [--copy <dir> | --store <file> --state <dir>]
```

`director reload` asks the session supervisor to end the runtime and start a new session, on the copy's current settings, as [release upgrades](releases.md#guildhall-director-reload) design.
It records a `director-reload` request and prints it as one JSON line; requests made before one is applied collapse into it, and `--now` turns a pending plain request into a `--now` one.
The supervisor applies a plain reload at a quiet point: a turn has ended since the last turn started and since the newest wake reached the Director, so a reload asked for during a turn the captain started waits for that turn to end, or for the Director to count as [idle](#the-end-of-a-turn) after `idle_after` with no turn signal.
A Director stage attempt running, such as a grill waiting on the Guildmaster's answers, does not hold a plain reload either, since the next session restarts it ([decision](decisions/plain-reload-ignores-director-stages.md), as [D94](decisions/0094-director-notes.md) ruled for `--now`).
`--now` applies it at the [end of the current turn](#the-end-of-a-turn); it is the last step of the [handoff](#handoff): check the notes, then reload.
`--now` is refused with exit 1 only when there is no session or the newest one is `stopped`; it needs no note, and a Director stage attempt running, such as a grill, does not hold it, since the next session restarts the grill ([D94](decisions/0094-director-notes.md)).
The [status feed](#status)'s session section carries the newest request as `reload`, `requested` or `applied` with the old and new session numbers, the two sessions' releases and binary sha256 (`old_release`, `old_sha256`, `new_release`, `new_sha256`) and, when the supervisor refused a new binary, `refused_sha256` and `refused_reason`.

### The supervisor's binary

The supervisor hashes the binary image it runs once when it starts, `/proc/self/exe` on Linux, so a binary an install renamed into place since is not mistaken for it, and records the sha256 on each session as `binary_sha256`.
At a reload on Unix it resolves `guildhall` on `PATH` and hashes it:

- The same sha256: it reloads in the same process, as above.
- Another sha256: it pre-flights that binary as a child, `<new> version --json` for its release, then `<new> director start --check --copy <dir> [runtime]`, which runs the full [preflight](#start) over the same copy, the store's schema among it.
  When both pass, it says so in its pane, removes the session's temporary skills, stops handling signals and replaces its own image with the new binary by `exec`, with its own arguments and environment.
  The new image keeps the process and its id (on Linux its whole recorded identity, as an `exec` changes neither the boot id nor the start time), the terminal and the Herdr pane, runs `director start` as usual, supersedes the stopped session `<n>` and records `<n+1>`, which applies the reload with the old and new release and sha256.
  It re-renders the session from the new binary's skills and baseline role, so nothing of the old binary is left in the Director.
- A refused binary, one that fails its pre-flight, cannot be found or hashed, or whose `exec` itself fails, is never moved onto: the supervisor stays on its own binary, reloads in the same process, prints one line naming the refused binary and the reason to its pane, and records the reload `applied` with `refused_sha256` and `refused_reason`.
  [`director status`](#status) shows the refusal on the session's `reload`, and the new session's `guildhall version --startup` warns of it, so the Director tells the Guildmaster at its first turn.

Running Artisans are never touched by a reload; they and their resume commands run whichever `guildhall` they run, from `PATH`, until release pins exist.
A new binary that passes its pre-flight and then fails after the `exec`, or a signal that arrives between the `exec` and the new image's handling of it, ends the supervisor and gives the pane back to its shell with session `<n>` `stopped` and the reload still requested; the next `director start` applies it.
On Windows there is no `exec`: a reload always stays on the supervisor's binary.

A session with no `binary_sha256` was recorded by a supervisor from a release before reloads moved binaries (#184), which only ever reloads in its own process and never looks at `PATH`.
Such a supervisor never moves at a reload, so the first upgrade onto #184 or later takes a `director stop`, then a `director start` from the new binary; a reload alone starts the next session on the old binary, recorded with the old release.
The status feed's `supervisor_binary` reports such a session with `differs` true and a `summary` saying so, and the new session's `guildhall version --startup` warns of it when the release differs.

`guildhall version --startup` compares its own binary's sha256 with the one the session's supervisor recorded and warns when they differ, even for two `dev` builds; for a supervisor that recorded none it compares releases.
The status feed's `supervisor_binary` compares the newest session's `binary_sha256` with the `guildhall` on `PATH`: `sha256`, `path`, `path_sha256`, `differs` and a one-line `summary` saying whether a reload would move the supervisor onto another binary.

## The end of a turn

A stop or a reload `--now` ends the runtime at the end of the Director's current turn, not in the middle of it.
A runtime with turn hooks says when a turn starts and ends: Claude Code's `UserPromptSubmit` hook runs `guildhall director turn-started` and its `Stop` and `StopFailure` hooks `guildhall director turn-ended`, the second for a turn that ends in an API error, each given the copy as `--copy '<copy>'`, single-quoted for a POSIX shell, so it does not depend on the directory Claude Code runs it in after the Director `cd`s; each finds the newest session from that copy and signals its supervisor over the session endpoint, an AF_UNIX socket at `supervisor.sock` in the session directory, bound as an [Artisan's endpoint](artisan.md#control-endpoint) is, on Windows as on Unix.
Each is exit 0 once the supervisor has answered and exit 1 otherwise, including for a usage error, because a `Stop` hook's exit 2 tells Claude Code to keep going and a `UserPromptSubmit` hook's exit 2 blocks the prompt.
Beside them, the `SessionStart`, `UserPromptSubmit`, `Stop` and `Notification` hooks arm `guildhall director wait-wake --copy '<copy>'`, Claude Code's async rewake hook, which waits on the same endpoint for a [wake](messaging.md#waking-the-director) and exits 2 to deliver it ([decision](decisions/director-wake-hook.md)); a rewake turn runs the `UserPromptSubmit` hooks as a typed prompt does.
The Director is idle while the newest of these signals is a turn end and no wake has reached it since: none has been typed into its pane, and none has been taken by its waiter, nor a waiter refreshed itself.
A turn the captain interrupts with Esc runs no hook at its end, so a runtime with hooks also counts as idle once no turn signal and no wake has arrived for [`[director] idle_after`](configuration.md#the-director-table), 5 minutes unless set; a turn that runs that long without a signal counts as idle too.
The supervisor decides at each signal and about once a second:

- `stop --now` ends the runtime at once;
- a stop or a reload `--now` ends it at the next turn end, or once 60 seconds have passed, when the runtime has no hook or the Director is idle;
- a plain reload ends it at a quiet point, or once 60 seconds have passed when the runtime has no hook;
- a stop wins over a reload.

`director stop --now` is the escape when the runtime must end before then: it ends the runtime at once, mid-turn if need be.

The fake runtime has no hooks, so its sessions wait out the 60 seconds unless their script runs `turn-started` and `turn-ended` itself; without a turn-start hook a turn the captain starts is not seen, so once a turn has ended the Director counts as idle until a wake types into its pane.
It is the one runtime a wake is typed into.

A Claude Code session keeps one waiter registered with its supervisor: a newer registration retires the older, which exits 0, and a waiter whose supervisor is restarting polls again rather than exit.
Five minutes before its 24-hour hook timeout a waiter still registered exits 2 with a counts-only line, `guildhall: <n> unread ...`, which the supervisor counts as a wake, so the short turn that follows arms a new one through its `Stop` hook and an idle Director is never left without a waiter.
A waiter only ends without a replacement when it is killed: while none is registered and messages are unread, the supervisor shows the wake notice in the pane's title, outside the input box, and hands the next waiter to register one wake for what it missed.
What another runtime would need for the same is a [design note](director-input-runtimes.md).
A session directory whose socket path would pass the platform's AF_UNIX limit gets no endpoint; the supervisor says so when it starts and waits out the 60 seconds as for a runtime without a hook.

## Automatic handoff

```sh
guildhall director context-report [--lanterns] [--copy <dir>]
```

Three triggers tell the Director a handoff is due before its context runs out ([decision](decisions/0085-director-handoff-triggers.md)); each is a [`[director]` setting](configuration.md#the-director-table) and 0 turns it off:

- `handoff_after_tasks` (off unless set): every that many tasks moved to `done` or `cancelled` since the session started;
- `handoff_context_percent` (70 unless set): the context the runtime last reported as used reaches the percent;
- `handoff_idle` (off unless set): the Director has had no turn signal and no wake for that long and, with turn hooks, its turn is over.

A trigger never ends the session itself: it asks the Director to run the [`guildhall-handoff`](#skills) skill, which runs and records the [note audit](#maintenance) and then runs `director reload --now`.
The first two ride on a [wake](messaging.md#waking-the-director): when a wake delivers its line and a trigger holds, the line ends `; handoff due (<reason>)`, such as `guildhall: 2 unread (question 2); handoff due (context 72%)`, with two reasons joined by `, ` when both hold, so no model turn is spent on the reminder alone.
The idle trigger is the session supervisor's one timer: once the Director has been quiet for `handoff_idle` it wakes it with the same line, the unread counts with `; handoff due (idle <duration>)`, handed to a Claude Code session's waiter and typed only into the fake runtime's pane.
Each crossing is announced once and recorded as a [handoff due](store.md#handoff-dues): the task trigger's crossings are every `handoff_after_tasks` finished tasks, so a Director that carries on is reminded again after as many more; the context trigger crosses once per session; the idle trigger once per quiet period, which a turn signal or a wake ends.
A wake that fails to deliver records no crossing, so the next one carries it.
The thresholds are recorded in the session when it starts, so a changed setting takes effect at the next session.

The context percent comes from the runtime, an optional capability (R1.24).
For Claude Code, the core `director turn-ended` hook handles both `Stop` and `StopFailure` and reads the hook input's absolute `transcript_path` before signaling the supervisor.
It writes the latest main-session assistant message's input tokens plus cache-creation and cache-read input tokens, divided by the model's context window and rounded, to `context.json` in the session directory.
It does not sum historical usage, count output tokens or include sidechain usage; a compaction boundary resets the previous reading.
Supported model IDs and explicit window suffixes are defined by `modelContextWindow` in [`internal/adapter/claude/context.go`](../internal/adapter/claude/context.go); unknown/provider-specific model IDs invalidate the prior report rather than guessing a capacity.
A successfully read transcript with no current known usage removes `context.json`, including after compaction; a transient read failure leaves the prior report untouched.
Malformed transcript rows are ignored; invalid hook input or a failed transcript read leaves the previous report alone.
Context refresh failure never suppresses the turn-ended signal.
Transcript reads are bounded to 128 MiB with 8 MiB per line.
This core hook is independent of [Lanterns](lanterns.md): removing the plugin, replacing the status line or disabling function hooks cannot remove the context trigger.

Generated workspaces use the display-only [Lanterns status line](lanterns.md#reply-lights-and-the-status-line), which never writes `context.json`.
The original `director context-report` without `--lanterns` retains its legacy behavior for existing workspaces: it writes the supplied percent to the session its [copy](running.md#copies) and `GUILDHALL_SESSION` name and prints `context <n>%`, opening no store.
In that legacy mode, input with no percent yet writes and prints nothing; invalid JSON or no session number is exit 1.
A runtime without the capability, such as the fake, records no report path and its context trigger never fires.
The transcript-hook integration test proves the context threshold with no plugin or status line installed.
The [Lanterns page](lanterns.md) describes its automatic installation, optional function-hook visuals and vitals labels.

## Guard rails

The guard rails are limits the binary enforces on a Director whatever it asks ([decision](decisions/0074-guard-rails.md)); `config.GuardRails` reads all three from the config.
In a standalone copy they are the copy's own config, which the Guildmaster edits; managed mode will make them read-only to the Director ([modes](director-modes.md#guard-rails)).

- Allowed repositories: the config's `[[repository]]` entries.
  `task new`, `task start`, `step done`, `step finish`, `step retry` and `messages send` refuse any other repository with exit 1, `guard rail repositories: the config does not list repository "<id>"`, before writing anything.
  `messages send` checks the envelope's repository when it has a config: the copy's, or one given with `--config`.
- Merge rule: each repository's [merge requirements](configuration.md#merge-requirements), which [deliver](interpreter.md#publish-and-deliver) enforces.
  A required review approval or CI pass is never merged over; with `guildmaster_approval` off, Guildhall merges by itself only when every other requirement holds and records what allowed it as `deliver-<k>-authorized`.
- Attempt caps: [`max_running_attempts`](configuration.md#settings), default 4, and `max_attempts_per_day`, default 40, across all the Director's repositories.
  `max_attempts_per_day = "0"` is no daily cap: no attempt is refused for the day's count, which is still counted and shown; `max_running_attempts` is at least 1.
  An attempt runs from its start to its finish, and a day is a UTC day.
  A [Director stage](interpreter.md#director-stages), which the Director runs in its own session, never counts against `max_running_attempts` and is never queued; it still counts toward the day's starts.
  The caps count attempts, not tokens or cost: each attempt's [spend](#spend) is recorded and shown beside them, but no budget refuses an attempt by its tokens or cost yet.

An advance whose next attempt the running cap does not admit, from `task start`, `step done`, `step finish`, `step retry`, `step send-back` or a launcher's own advance, is queued instead of refused ([decision](decisions/queue-over-cap-starts.md)): it launches nothing, records the step's place in the [start queue](#the-start-queue), sends no message and exits 0, printing `{"wait":"queued","step":"<step>","attempt":<n>,"position":<p>}`.
The session supervisor starts it when a slot frees and it is the queue's front, with no command from the Director.
An advance whose next attempt would pass `max_attempts_per_day` records and launches nothing and exits 1 naming the rail, `guard rail max_attempts_per_day: ...`, and sends the Director a `budget-refused` message, once per task and UTC day; once the day turns the Director runs `guildhall task start` for the task again, or `step retry` again for a refused retry.
The count, the queue and the start are one store write, so two advances never both take the last place or pass the queue's front, and an attempt already recorded is always launched again after a crash.

### The start queue

The start queue holds every start the running cap did not admit, at most one per task and step, across all the Director's repositories.
It is ordered by each task's current [priority](tasks.md#priority), then by the time the start was queued, then by task id, so `task priority` reorders it at once.
A start is admitted only while fewer than `max_running_attempts` attempts run and it is the queue's first entry: a task whose own attempt just finished queues behind the starts already waiting rather than taking the freed slot.
Nothing is preempted; a p0 only goes to the front.

The session supervisor's start-queue watcher, beside the [checks watcher](#checks), drains it every `checks_refresh`, never more often than every 30 seconds: each round advances the queued tasks from the front, as `task start` does, and stops at the first that stays queued.
A freed slot can therefore sit idle for up to about 30 seconds.
A queued start whose advance neither starts nor queues it, such as one the [dependency gate](#dependencies) now holds or one whose advance fails, leaves the queue, logged to `start-queue.log` in the session directory, and the task's `next.json` reports that advance as any other; if the task still waits queued, `needs_me` lists it as an `advance` item.
Like the other watchers it advances nothing while the session is paused, stopping or sweeping.

Repeating the same `task start`, `step retry` or `step send-back` while it is queued keeps its entry and prints its place, which holds at the time of the command only.
A `step retry` naming another attempt is refused with exit 1, naming the queued attempt, and a `step send-back` with other feedback is refused as already having prepared inputs ([send-back](step-send-back.md)).
A queued retry keeps its first reason, which its prompt quotes and which is sent as `attempt-retried` once the attempt launches.
Quality-judge starts are not queued: `quality judge` is still refused at the running cap.

## Spend

Each owned attempt's store record holds what its runtime reported it spent ([decision](decisions/0079-attempt-spend.md)): input, output, cache creation and cache read tokens and a cost in US dollars, each marked `measured`, `estimated` or `unknown`, from the attempt's [usage file](adapters.md#usage-file).
The launcher records it when the attempt ends, and `artisan resume` when it settles an exited launcher's attempt.

What is measured:

- The [Claude adapter](adapters.md#claude-adapter): tokens are measured, the counts Claude Code reports for the session; the cost is estimated, Claude Code's own price for those tokens, not a bill.
  A Claude session that was stopped reports nothing, so its spend is unknown though it spent tokens.
- The [fake adapter](adapters.md#fake-adapter) reports fixed numbers for tests.
- A runtime that reports nothing, a usage file Guildhall rejects, and a [Director stage](interpreter.md#director-stages), which the Director runs in its own session, are unknown: zero numbers marked `unknown`, never a guess.
- The Director's own session is not metered.

[`director status`](#status) sums the spend per task and per UTC day, each total counting the attempts whose tokens or cost are unknown instead of adding a guess.

## Safety checks

Four checks in `cmd/guildhall` drive the built binary through `director start` with a stand-in runtime playing the Director from its role prompt's script, on every CI runner and with no model calls ([decision](decisions/0080-director-safety-checks.md)).
The fake Director and the Claude stub play the same script, so each check runs on both.
Each check counts the stand-in's turns, one for its first message and one for each line typed to it, and the fake Artisans' recorded processes, before and after, and ends only when every one of those processes is gone.

- Approval: the Director's `task new` on a workflow below the review floor is refused; it approves the workflow with words, and the same `task new` succeeds.
- Guard rails: with `max_running_attempts = "1"`, a `task new` for an unlisted repository is refused, a second task's start is queued while the first runs, printing `{"wait":"queued",...}` and shown in `director status --json` under `start_queue`, no `budget-refused` message is sent, and the session's start-queue watcher starts it by itself once the first attempt is stopped, with no second `task start`.
- Crash recovery: with a task's attempt running, the Director's runtime is killed, then the attempt's launcher, and the Guildmaster sends a note.
  `director status --json` lists the attempt in `needs_me` with `resume: true`.
  `director start` again renders a `startup.md` that names the attempt to resume and the unread message, `task new` is refused until the Director runs `director ready`, and the Director resumes and retries the attempt.
  The watcher then takes the task through the rest of the fake-straight workflow, the check answering each decision as the Director would, with the same `decide answer` and `task start --decision` commands, to the build attempt, the sign-off and the deliver step's merge decision, the workflow's final wait.
- Zero idle turns: with one open decision and nothing else, a ready Director whose session is bound to a Herdr pane takes no turn and is sent no wake over a 30 s hold.
  A Guildmaster note then records exactly one wake, `woke`, which types one line, the [wake line](messaging.md#waking-the-director), into the pane, and the runtime takes exactly one turn from it.
  A stand-in `herdr` on PATH plays the pane, as no Herdr server runs in CI.

A managed copy's guard rails, a Director-edited `managed.toml` refused with an alert, are checked when managed mode lands.

## Status

```sh
guildhall director status --json [--copy <dir> | --store <file> --state <dir> [--config <file>]]
```

`director status` is the Director's status feed and the Guildmaster's "what needs me" query in one read-only JSON document ([decision](decisions/0065-director-status-feed.md)).
firstmate's external monitor reads it, the Director's skills run it at every turn, and the session supervisor renders its `needs_me` into a new session's [startup instructions](#start).

It reads the store in one read transaction, opened read-only and waiting up to 30 seconds for a writer's lock, the default [`lock_wait`](configuration.md#settings) of an advance, so it never writes, never creates a store and succeeds once a writer lets go.
It then reads files under the state directory: each task's newest `next.json` and the attempt directories of running attempts, which it classifies with [`reconcile.Classify`](artisan.md#reconcile) and the launcher identity probe; classifying may create an attempt's `attempt.lock`, as `artisan status` does.
It prints one JSON line and exits 0; a missing store is exit 1 naming `guildhall store init`, a store that cannot be read is exit 1, and a usage error, including a missing `--json`, is exit 2.
With a [copy](running.md#copies), `--copy <dir>` or the copy found from the current directory stands in for `--store`, `--state` and `--config`.

The document has these sections:

- `as_of`: labelled like the [task inspection reads](tasks.md#inspection).
  `partial` is always `true`; `excluded` names what the feed does not inspect: `worker files`, `transcripts` and `forge state`.
  `sequence` and `committed_at` are the newest mutation committed anywhere in the store when the read began; an empty store has sequence 0.
  Live attempt state is read after the store, so it can be newer than `sequence`.
- `copy`: the absolute copy directory, when the feed was read from a [copy](running.md#copies) rather than explicit paths; skills take paths under the copy, such as its `quality/` directory, from it.
- `session`: the newest Director session: its `state`, `number`, supervisor `identity`, `release`, `pane` when bound, `started_at`, `ready_at` and `ended_at` once set, `stop_now` for a `stop --now`, `sweeping` while a [stop](#stop) has not finished stopping its attempts, the `end_reason` of a session [stopped](#stop) as `superseded`, and `context`, the newest [context report](#automatic-handoff) with its `used_percent` and `reported_at`, absent when the runtime reported none.
  `last_wake` is the newest [wake](messaging.md#waking-the-director) of any message to the Director, with its `message_id`, `sender_type`, `decision`, `reason` when queued, `time` and `sequence`, so the feed says whether the last message woke anyone and why not; it is absent before any wake. `sender_type` is `guildhall` or `local`; a message header's `sender` is the finer field.
  A store with no session reports only its `state`, `"no session"`, `last_wake`, `guard_rails` and `start_queue`.
  Feed change ([D94](decisions/0094-director-notes.md)): the session no longer carries `handoff_note`, the newest retired handoff note, and does not carry the Director's [notes](#notes) either; `director note list` prints them.
  `guard_rails` are the [guard rails](#guard-rails): the config's `repositories`, `merge_requires` (each repository's effective `guildmaster_approval`, `artisan_approval`, `ci_green` and `nothing_open`), `max_running_attempts` and `max_attempts_per_day` (0 when there is no daily cap), when the copy has a config, and always `running_attempts`, what the running cap counts, the running attempts other than Director stages, `attempts_today` and the UTC `day` they count, and `spend_today`, the [spend total](#spend-totals) of the attempts started that day, read in the same store transaction.
  `start_queue` is the [start queue](#the-start-queue) in queue order, `[{repository, task, step, attempt, request, priority, position, queued_at}]`, `request` being the `start`, `decision`, `retry`, `send-back` or `engine` request that queued it and `priority` the task's current one; it is empty when nothing waits.
  A queued start is the supervisor's to start, so it is not a `needs_me` item and wakes nobody.
  A config that exists but cannot be read gives `config_error` in place of the rails.
  The identity's liveness is not probed yet.
- `mode`: `{"name":"standalone"}` until managed mode lands.
- `quality`: the effective [quality settings](configuration.md#quality-settings), `self_review`, `size_budget_lines`, `size_budget_files`, `audit_sample` (the deprecated `quality_audit_sample`, which has no effect since the Guildmaster's audit was [retired](decisions/retire-quality-audit.md)), `reference_runs`, `reference_runs_per_task`, `reference_cost_cap_usd` when set, `judge_separate_family`, `judge_profiles` when set and `skill_edits_need_approval`, when the copy has a config; absent when it has none or its config cannot be read, which `guard_rails` reports as `config_error`.
- `tasks`: every task in repository then id order, with its `state` (`open`, `done` or `cancelled`), bound `workflow`, and:
  - `next`: the `next.json` the task's last advance wrote beside its task lock, at `<state>/<repo>/<task>/next.json`, copied as JSON exactly as written; absent when there is none.
    A file that is not JSON is reported as `next_error` instead.
    Every `task start`, `step done`, `step finish` and `step retry`, typed or run by the [watcher](running.md#the-watcher), writes it, so it is absent only for a task never advanced.
  - `steps`: for each step with an attempt, in the order the steps first started, its latest `attempt` with its `outcome`, or, while it runs, `live`: the classification `state` and `reason` from `reconcile.Classify`.
    A running attempt with neither an ownership record nor an attempt directory is `absent`, as `artisan status` reports it.
    A [Director stage](interpreter.md#director-stages)'s attempt, which the engine handed to the Director and which has no ownership record, is `handed`, reason `handed to the Director`, so a stage that is the Director's to run is not read as a lost attempt.
    The classification is measured by the feed's clock, so a heartbeat's age and whether it is `stuck` follow it; each such attempt, other than a Director stage, is also listed under `running` with its evidence.
    A latest attempt with a counting park, as listed under `parked`, has `"parked": true`, which is why its failed or stopped step is not in `needs_me`.
  - `spend`: the [spend total](#spend-totals) of the task's attempts.
  - `overrides`: `true` when the task has a [task override](tasks.md#task-overrides) in effect, absent otherwise; `task show` lists them.
- `merge_queues`: each repository's [merge queue](interpreter.md#the-merge-queue), `[{repository, entries}]` in repository order, with the entries front first and then in queue order.
  Each entry has its `task`, `priority`, `ready_at` (the commit time of the receipt that first authorized its merge), `pr` (the pull request URL), `held` (a [`task hold-merge`](tasks.md#holding-a-merge)), `front`, and `waiting_on`: `rebase` while the front's head is being rebased or resolved, `ci` while the front waits for the checks of its rebased head, `decision` while a new merge decision for it is open, and `queue` for a task behind the front.
  `task list --json` gives each queued task the same entry as `merge_queue`.
- `decisions`: every open decision in repository, task and id order, with its `kind`, `subject`, `options`, `question` and `opened_at`, the commit time of its opening.
  A decision [deferred](decisions-cli.md#deferring-a-decision) whose tasks have not all merged has `"deferred": true`; the Lanterns band does not count it.
- `messages`: the Director's `unread` count, the count `by_type`, and the `headers` of at most the 20 oldest unread messages (`id`, `type`, `sender`, `repository`, `task_id`, `step` and `attempt` when given, `sequence`); `sender` is the message's [sender](messaging.md#senders), `unknown` for a message stored without one.
- `recent`: the last 20 receipts of any task across repositories, in commit order, each with its operation, mutation id, sequence, commit time and record name, so records such as delivery's `publish-<k>-pr` documents and its `merged`, `merge-refused` and `workspace-kept` messages appear without a change to the feed.
- `needs_me`: what is waiting on the Director or the Guildmaster, each item with its `kind`, the task and attempt or decision it concerns, a `reason`, and the `command` the Director would run next, written without `--copy` because a Director session runs inside its copy and finds it by [copy discovery](running.md#copies).
  Items come in this order, and within a kind by repository, task, then step or message order:
  1. `advance`: an open task's step whose latest attempt is finished while the task's `next.json` still says `"wait":"attempt"` for that attempt, as a [watcher](running.md#the-watcher) whose advance failed leaves it; `guildhall step done --repo <r> --task <t> --step <s> --attempt <n> --result <file>` for a `succeeded` attempt whose result file exists, otherwise `guildhall task start --repo <r> <t>`.
     A [Director stage](interpreter.md#director-stages) whose attempt is unfinished but has a published result, as a `step finish` that stopped before its advance leaves it, is also an `advance` item, with that `step done` command, whether or not its stage message was read.
     Every settled [judge](quality.md#running-it) attempt without a durable intake notification receipt is also an `advance` item, even on a done task or after newer judge attempts: `guildhall quality judged --repo <r> --task <t> --attempt <n>`.
     An acknowledged intake notification still counts as completed intake.
  2. `retry`, `retryable: true`: an open task's step whose latest attempt finished `failed` or `stopped`; `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>`, to which the Director adds `--reason`.
     This includes an attempt whose result intake was rejected after launcher success; its `reason` includes the stored rejection reason, and the task waits for the Director.
     A legacy succeeded attempt with unfinished intake first appears as `advance`; taking its result applies the rejection and makes it retryable.
     An unfinished judge without ownership or a launch marker is also a `retry` item, even on a done task: `guildhall quality judge --repo <r> --review <n> <t>` uses the published preparation's review number.
     Without a readable published review number, the command omits `--review` and the reason explicitly requests that the Director supply it; partial preparation is not used.
     A [parked](running.md#step-park-and-step-unpark) attempt is left out and listed under `parked` instead.
     A park held `--until-merged` whose [hold](#holds) is released no longer counts, so its attempt is a `retry` item again, its `reason` ending with what released it and `; park reason: <reason>`.
  3. `resume`, `resume: true`: an unfinished attempt of an open task, or a judge attempt even on a done task, classified `stuck`, `exited` or `unknown`, or `finished`, `failed` or `cancelled` by files not yet settled in the store, as a launcher that died between writing `outcome.json` and recording it leaves them (not a Director stage, whose published result is an `advance` item); the `reason` is `<state>: <classification reason>`; `guildhall artisan resume --repo <r> --task <t> --step <s> --attempt <n>`.
  4. `stalled`: an unread `worker-stalled` message; `guildhall messages read <id>`.
  5. `decision`: an open decision; `guildhall decide show --repo <r> --task <t> --decision <id>`.
     A decision deferred until tasks merge is left out while its [hold](#holds) waits and listed under `held`; once released its `reason` ends with what released it and `; deferral reason: <reason>`.
  6. `follow-up`: a released [follow-up](#holds), with its `follow_up` id and its text and what released it as the `reason`; `guildhall director follow-up done <id>`.
     It stays until done, wakes [`director resume`](#pause-and-resume) and counts as Lanterns attention.
  7. `messages`: one item counting the other unread messages; `guildhall messages list director`.
  8. `director`: an open task whose `next.json` says `"wait":"director"`; `guildhall task timeline --repo <r> --json <t>`.
     A task whose `next.json` waits for the Director on a parked attempt, with no decision, is left out; a wait for a decision or another attempt stays.
  9. `paused`: an open task whose `next.json` says `"wait":"paused"`, held while the Director was [paused](#pause-and-resume); `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>` when that step's latest attempt failed or stopped, or `guildhall task start --repo <r> <t>` otherwise, including recovery of a recorded-but-unlaunched attempt.
  10. `checks`: an open task whose `next.json` says `"wait":"checks"`, with its `step`, which the session supervisor's [checks watcher](#checks) advances; `guildhall task start --repo <r> <t>`.
  11. `dependencies`: an open task whose `next.json` says `"wait":"dependencies"`, with its held `step`, which the [dependency gate](interpreter.md#the-dependency-gate) holds until its dependencies merge and the supervisor's [dependency watcher](#dependencies) advances; `guildhall task start --repo <r> <t>`, or `--ignore-deps --reason <text>` added to start it anyway.
  12. `maintenance`: a due [note audit](#maintenance), then one item per repository, in id order, whose hallmark is due, with its `repository` and the `count` of merges since; after the `merge-queue` items, and before only `install`.
      It is the Director's but not urgent, so it does not wake the Director on [resume](#pause-and-resume) and does not hold back `director ready`.
  13. `install`: last, after every other item, one item when the binary printing the feed is behind Guildhall's own repository's default branch, its `version` block's `behind_main` with a `count` above 0, with that `repository` and `count` and the reason `the installed guildhall (<first 8 of the revision>) is <n> merge[s] behind main[ (#<pr>, …)]; ask the Guildmaster to install`; `guildhall version`.
      It is read from the default branch as last fetched, with no fetch, and is not stored, so it changes on every read and is gone once an installed binary is no longer behind; a binary with no revision, or a copy that lists no own repository, has none.
      The advance that leaves a task of Guildhall's own repository done with a merge fetches that branch first, bounded and ignoring failure, so the feed the `merged` message wakes the Director for counts it; a merge made by hand is counted once anything fetches the branch, as plain `guildhall version` does.
      It waits on the Guildmaster, so it neither counts nor wakes the Director on [resume](#pause-and-resume), and the supervisor's snapshot, which has no `version` block, leaves it out.

  The newest Director session, while it is `recovering`, comes before all of them as a `recovering` item; `guildhall director ready`.
  While it is `recovering` or `running` with no bound Herdr pane, a `no_pane` item follows, since no wake can reach it and messages wait until its next turn; `guildhall director start --tab`, once this session is stopped ([start](#start)).
  Done and cancelled tasks contribute no `advance`, `retry`, `resume`, `director`, `paused`, `checks` or `dependencies` item.
  A [cancelled](tasks.md#task-cancel) task's decisions are void, so it contributes no `decision` item either: only its unread messages count, in `messages`, until they are read.
  A park leaves an `advance` or `paused` item in place: a failure never taken still needs taking, and a retry asked for while paused wins over a park.
- `parked`: the attempts the Director [parked](running.md#step-park-and-step-unpark), held on purpose and so not in `needs_me`, in repository, task, step and attempt order, each with its `repository`, `task_id`, `step`, `attempt`, `outcome`, `reason`, `parked_at`, the commit time of its newest park, and the `command` to run when the hold ends, `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>`.
  A park counts only while it is parked, its task is open and its attempt is still the step's latest, finished `failed` or `stopped`; a park whose attempt was retried, whose task closed or that was unparked is ignored ([decision](decisions/0092-park-attempt.md)).
  A park held `--until-merged` also has `until_merged`, the tasks it names, and `waiting_for`, those not merged yet, and stops counting once its [hold](#holds) is released.
- `held`: the deferred decisions and follow-ups whose [holds](#holds) still wait, in kind, repository, task and item order (follow-ups by id), each with its `kind` (`decision` or `follow-up`), `repository` and `task_id` for a decision, `item` (the decision or follow-up id), `reason` (the deferral's reason or the follow-up's text), `until_merged`, `waiting_for` and the `command` that lifts it: `guildhall decide undefer --repo <r> --task <t> --decision <id>` or `guildhall director follow-up done <id>`.
- `running`: every attempt with no store outcome that `steps[].live` classifies, the latest of each step and every judge attempt, other than a `handed` [Director stage](interpreter.md#director-stages), in repository, task, step and attempt order, as information rather than work ([decision](decisions/running-attempts-in-status.md)).
  Each has its `repository`, `task_id`, `step`, `attempt`, classification `state` and `reason`, `launcher` and `endpoint` when the classification asked them, the last heartbeat's `worker_state`, `heartbeat_at` and `last_output_at` (absent with no heartbeat), and the `command` that classifies it again, `guildhall artisan status --repo <r> --task <t> --step <s> --attempt <n>`.
  It adds no probe: it is the classification `steps[].live` already made.
  One that is not healthy is also a `needs_me` item: `stuck`, `exited`, `unknown` and settled-on-disk attempts as `resume`, by the rule above, and a silent worker, through its launcher's `worker-stalled` message, as `stalled`.
  An `absent` attempt, the moment between an advance and its launcher taking ownership, is listed but not flagged, and attempts of done or cancelled tasks are listed while their store outcome is empty.
- `grants`: every active [task grant](tasks.md#task-grants), in repository, task and grant order, each with its `repository`, `task_id`, `grant` id, `step`, `rules`, `reason`, `until`, the attempts `bound` with it so far and `approved_at`, the commit time of its decision's answer.
  A grant leaves the list once it expires, its task closing or its one bound attempt having an outcome, or is revoked, with no write ([decision](decisions/task-grants.md)); a pending grant is in `needs_me` as its open `grant-<id>` decision instead.
- `maintenance`: when [maintenance](#maintenance) was last run and is next due under the newest session's settings, absent until a session was started with a maintenance policy: `baseline_session`; `note_audit` with `every_sessions`, the last audit's `last_session` and `last_at` once one is recorded, `sessions_since` and `sessions_until_due`, absent when the audit is off; and `hallmarks`, one per repository in id order, with its `repository`, `after_merges`, the last hallmark's `last_task` and `last_at`, the time its task closed, once there is one, `merged_since`, `merges_until_due`, absent when hallmarks are off, and `running_task`, the open hallmark task, absent when none is open.
  It carries counts and times only, never a note's text, so notes stay out of the feed ([decision](decisions/director-maintenance.md)); `director maintenance list` shows the history.
- `repository_policies`: each configured repository's [policies](configuration.md#repository-policies), read from its default branch as last fetched into `refs/guildhall/policies` with no fetch, in config order: `{"repository", "commit", "policies": [...]}`, `policies` empty for a repository without a policy file; `{"repository", "error"}` when its file is refused or cannot be read; or `{"repository", "not_read_yet": true}` before the first fetch.
  The field is absent when no repository has a policy file and none has an error, and the session supervisor's own snapshot leaves it out.
  Policies are guidance, not [guard rails](#guard-rails), which are limits the binary enforces, so they are not under `guard_rails`.
- `version`: the binary printing the feed, the object `guildhall version --json` prints ([version](contracts/cli.md#version)): its `revision`, `time`, `modified`, `go_version`, `platform`, the `pr` and `task` of its latest merge and `behind_main`, read from the repository's last fetched default branch with no fetch, its `as_of` naming when that fetch was.

Times are commit times of the store's receipts, in UTC.
The feed reads every receipt in the store, so its cost grows with the store's mutation count, as `task timeline`'s does.

### Spend totals

A spend total sums the [spend](#spend) of every finished attempt, and of a running one that has recorded its spend: `attempts` counts them, `tokens_unknown` and `cost_unknown` count those whose tokens or cost are unknown, which add nothing, and `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens` and `cost_usd` are the sums.
`tokens` and `cost` are the sums' measures: `unknown` when no attempt's number is known, `estimated` when any summed number is, otherwise `measured`.
A task or day with no such attempt has `attempts` 0 and both measures `unknown`.
