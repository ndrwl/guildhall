# The Director

The Director is the agent that runs a Director copy: it gives Artisans work, answers their messages and asks the Guildmaster for decisions ([architecture](architecture.md)).
This page describes the commands that serve the Director itself rather than one task.
`director status`, the [session record](store.md#director-sessions), the session supervisor `director start` that writes it, the session's [Herdr tab](#the-directors-tab), `director ready`, `director pause`, `director resume`, `director stop`, `director handoff`, `director reload`, `director turn-started`, `director turn-ended`, the [automatic handoff](#automatic-handoff) triggers with `director context-report`, and the standalone [guard rails](#guard-rails) are built.
A message to the Director [wakes it](messaging.md#waking-the-director) when its session is running in a pane Guildhall created.

## Start

```sh
guildhall director start [runtime] [--check] [--copy <dir>]
```

`director start` is the session supervisor: it runs one Director session in the current terminal and records it ([decision](decisions/0071-director-start.md)).
With `--tab`, `director start` instead opens the Director's own [Herdr tab](#the-directors-tab) and runs the supervisor there.
It follows the [role grammar](decisions/0053-cli-role-grammar.md): the runtime is the positional argument, else the config's [`[director] adapter`](configuration.md#the-director-table), and a copy without either is refused with exit 1 listing the adapters.
It needs a [copy](running.md#copies), from `--copy` or `GUILDHALL_COPY`, because the session lives in it.

It first runs a preflight and prints one line per check, as `doctor` prints, stopping at the first that fails:

- `config`: the copy's `config.toml` is a Director config.
  Until managed mode lands, a copy with `managed.toml` beside its config, or with inline `[managed]` settings, is refused naming `guildhall director enrol`.
- `runtime`: the runtime is one of the adapters, and the program it runs is on `PATH`; the fake runtime is the `guildhall` executable itself.
- `store`: the copy's `director.store` exists and opens; a missing one is refused naming `guildhall store init`.
- `skills`: the release skills and baseline install.
- `artisans`: names each profile that runs the claude adapter, whose print mode allows edits only, so a build's checks and commits need Bash allowed by Claude's own settings ([Artisan permissions](adapters.md#artisan-permissions)); it refuses nothing.

A failed check is exit 1 with nothing recorded and nothing written in the copy, and `--check` exits 0 after the preflight, also recording nothing.

A start is refused with exit 1, naming `director: live` and recording nothing, while the newest session is not `stopped` and its supervisor's recorded identity names a live process, or cannot be shown gone.
A session whose supervisor is gone, such as one whose machine restarted, is superseded instead.
A start is refused with exit 1 the same way, naming `director: stop sweeping`, while the newest session's [stop](#stop) has not finished stopping its attempts: the session is `sweeping`, or `stopping` with its supervisor gone; wait for `director stop` to finish, or run it again to finish the sweep.

Then it records session `<n>`, one more than the newest, as `recovering` with its own process identity, release and config digest, which records an earlier session still recorded live as `stopped` with the end reason `superseded`.
`--herdr-session`, `--herdr-workspace`, `--herdr-tab` and `--herdr-pane`, given all together and never with `--tab`, are the pane binding the [tab wrapper](#the-directors-tab) passes; the session records them, and a [wake](messaging.md#waking-the-director) types into that pane.
It renders the session into `<copy>/director/session-<n>/` with [`render.Session`](rendering.md#director-sessions):

- `role.md`: the role prompt, from `[director] role` relative to the config directory, else the copy's `baseline/roles/director.md`, else the release's;
- every release skill, byte for byte, in the runtime's skills directory, `.claude/skills/` for Claude Code;
- the runtime's own files: for Claude Code, `.claude/settings.json`, which lets Bash run `guildhall` and read-only `git` commands without asking, denies any `git` command with `--output` (which writes a file), denies edits to the copy's `config.toml` and `managed.toml`, and registers a `UserPromptSubmit` hook that runs [`guildhall director turn-started`](#the-end-of-a-turn) at the start of every turn and `Stop` and `StopFailure` hooks that run `guildhall director turn-ended` at the end of every turn;
- `startup.md`: the startup instructions, saying the session is starting up and recovering, naming the newest [handoff note](#handoff) by its path, or saying there is none, and listing the in-flight snapshot, each `needs_me` item of the [status feed](#status) with its command, except a `checks` item, which says the session supervisor advances it ([checks](#checks));
- `manifest.json`: the digest of every file above.

It binds the session's [endpoint](#the-end-of-a-turn) and then runs the runtime's session command as its child in that directory, inside a containment unit of its own ([runtime containment](decisions/0084-director-stop-admission.md)), the same backend as an [Artisan's](artisan.md) (a Windows Job Object, a Linux cgroup or a process group with the supervisor as child subreaper, a macOS process group), so every process the runtime starts, such as its shell and tool children, is the session's, except the launchers of the attempts it starts, which leave the unit under their own [ownership](artisan.md) and [stop](#stop), with `GUILDHALL_COPY` set to the copy, the terminal's input and output, and the first message naming `startup.md`.
For Claude Code that is `claude --append-system-prompt-file role.md --add-dir <copy> [--model <model>] "<first message>"`; `[director] effort` is not passed, since Claude Code reads effort from its own settings.
The terminal's interrupt reaches the runtime, which handles it; the supervisor waits for the runtime to end, records the session `stopped`, and exits 0, or 1 naming the status when the runtime exited nonzero.
On Unix a hangup or termination of the supervisor, such as the terminal closing, is passed on to the runtime's unit as a termination; the supervisor waits for it to end, records the session `stopped`, and exits 1 naming the signal.
A signal the supervisor handles that arrives after the session is recorded but before the runtime starts starts no runtime: the session is recorded `stopped` and the supervisor exits 1 naming the signal.
A session already stopped or superseded by a newer one is left as it is.
While the runtime runs, the supervisor reads its session about once a second; once [`director stop`](#stop) has recorded it `stopping`, it ends the runtime at the [end of the turn](#the-end-of-a-turn), records the session `stopped` and exits 0.
On the same tick it runs the [checks watcher](#checks).
When it ends the runtime for a [reload](#reload) it records the session `stopped` with the end reason `reloaded`, reads the copy's config again and starts session `<n+1>` the same way in the same terminal, which records the reload `applied` from session `<n>` to `<n+1>`.
The config read again chooses the runtime too: unless `director start` named one, a changed `[director] adapter` takes effect, checked on `PATH` as the preflight checks it, and a runtime refused then ends the supervisor with exit 1, with session `<n>` recorded `stopped` as `reloaded` and the reload left requested.
A `director stop` recorded while the runtime is ending for the reload wins: the session is recorded `stopped` as for a stop and the supervisor exits 0 with no new session.
A runtime that ends by itself is recorded `stopped` and gives the terminal back, even with a reload requested; that reload, like one requested while no supervisor runs, is recorded `applied` by the next `director start`.
However the runtime ends, the supervisor kills whatever it left running in its unit and records the session `stopped` only once the unit is empty; a unit not proven empty within 10 seconds of the runtime's end makes the exit 1, naming the survivors.
On Linux the runtime is also killed if the supervisor dies.

## Skills

The release ships the Director's own skills beside the stage skills, each rendered into every session ([decision](decisions/0075-director-skills.md)):

- `guildhall-director-startup`: handle `startup.md`, work each in-flight item, then `director ready`.
- `guildhall-director-inbox`: on every wake line and turn start, read `director status` and each unread message, act and acknowledge.
- `guildhall-director-tasks`: turn what the Guildmaster says into commands, from giving work and answering decisions to merging, retrying, stopping, rulings and pausing.
- `guildhall-director-stage`: run a [Director stage](interpreter.md#director-stages) from its brief and finish it with `step finish`.
- `guildhall-handoff`: record a handoff note and reload; the Guildmaster can type `/guildhall-handoff`.
- `guildhall-self-review`: after a task merges, when the [quality settings](configuration.md#quality-settings) turn it on, record the task against the [quality rubric](quality.md) without changing it.

Between them they name every command of the [README's list](../README.md#the-commands) the Guildmaster would otherwise type ([D57](decisions/0057-director-conversation-first.md)).

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

Guildhall never closes the Director's tab: a failed start leaves it open with its output, and closing it at stop is `director stop`'s decision.
Herdr gives a new workspace a first tab of its own, which is left open, so on a new workspace the Director's tab is the second, not the first R1.37 asks for ([decision](decisions/0078-director-tab.md#not-yet)).

CI runs the wrapper over a terminal double whose panes run each typed line with `sh` on Linux and macOS, and over an in-process double on every platform; the [recorded Herdr responses](contracts/adapter.md) prove the Herdr client's side.
The acceptance is this lab checklist, not CI, in an isolated named Herdr session:

- zsh on WSL, Git Bash on native Windows, and PowerShell 5.1 on native Windows: `director start --tab` creates the workspace labelled with the Director's name once and reuses it, opens the `director <name>` tab without taking focus, the typed line starts the supervisor, and the printed session carries that tab's pane;
- a second `director start --tab` while that session runs is refused with `director: live`;
- after the supervisor is killed, `director start --tab` supersedes the old session and opens a new tab;
- a message to the Director types its wake line into the new tab.

The checklist has not been run yet; macOS stays CI-only (R1.30).

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

- `task start` and `step retry` print `{"wait":"paused"}` and exit 0, recording nothing.
- `task start --decision` still feeds the Guildmaster's answer, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `step done` still takes the attempt's result and opens any decision it leads to, but starts no next attempt: it prints `{"wait":"paused","step":"<step>"}`, which its `next.json` records, and [`needs_me`](#status) lists the task as `paused`.
- `decide answer`, `messages`, `artisan stop` and every read are unaffected.

`director resume` moves a paused session back to `running` and prints one JSON line, `{"session":{...},"needs_me":[...]}`, with the [status feed](#status)'s `needs_me`, which the Director then works, such as the `task start` of each held task.
It is the Director's resume, not [`artisan resume`](artisan.md#commands), which settles one attempt whose launcher is gone.

A resume that moves the session wakes the Director once for what waited while it was paused, such as messages queued `paused` and held tasks: when `needs_me` holds anything for the Director, anything but `checks` and `decision` items, and the session has a bound pane, it types one line of counts by kind, never any text, and prints `"wake":{"decision":"woke"}` in its line:

```text
guildhall: resumed, 2 waiting (messages 1, paused 1)
```

Otherwise it types nothing and prints `"wake":{"decision":"queued","reason":"<reason>"}`, `nothing waiting` or one of the [wake](messaging.md#waking-the-director)'s session and pane reasons, and the work waits for the Director's next turn.
If reading the session for the wake fails, resume reports the error on standard error and omits `wake`, without failing or undoing the resume.
The resume records no wake: each message keeps the `queued` wake it recorded while the session was paused ([decision](decisions/0086-director-checks-watch.md)).
A `checks` item is the supervisor's and a `decision` item the Guildmaster's, so neither counts nor wakes.

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

A session whose supervisor's identity is gone cannot end its runtime, so `stop` records it `stopping`, stops its attempts and then records it `stopped` with the end reason `superseded`.
No session, or a session already `stopped` whose stop has finished its sweep, is exit 1.

The stop does not yet send the Director a `stop-requested` message asking it to write a [handoff note](#handoff) first; that lands later.

## Handoff

```sh
guildhall director handoff --note-file <path|-> [--copy <dir> | --store <file> --state <dir>]
```

`director handoff` records a handoff note: what the next session needs to carry on, written by the Director before its session ends ([decision](decisions/0082-director-handoff-reload.md)).
The note is read from the file, or from standard input for `-`, with the rules of a [decision answer](decisions-cli.md#answers): a leading UTF-8 byte order mark is dropped, CRLF line endings become LF and UTF-16 is refused.
It must not be blank and is at most 64 KiB after that.
The note is a document, not a store record (R1.13): it is written to `<state>/documents/director/handoff-<seq>.md`, and the store records a [handoff note](store.md#handoff-notes) naming the newest session, the path relative to the document directory and the note's sha256.
It prints the record as one JSON line.
No session, or a newest session that is `stopped`, is exit 1 and records nothing; recording the same note twice in a row in the same session prints the first record, and recording it again after another note records it anew as the newest.
Each new session's `startup.md` names the newest note, and the [status feed](#status)'s session section carries it as `handoff_note`.

## Reload

```sh
guildhall director reload [--now] [--copy <dir> | --store <file> --state <dir>]
```

`director reload` asks the session supervisor to end the runtime and start a new session, on the copy's current settings, as [release upgrades](releases.md#guildhall-director-reload) design.
It records a `director-reload` request and prints it as one JSON line; requests made before one is applied collapse into it, and `--now` turns a pending plain request into a `--now` one.
The supervisor applies a plain reload at a quiet point: no Director stage attempt is running and a turn has ended since the last turn started and since the newest wake typed into the Director's pane, so a reload asked for during a turn the captain started waits for that turn to end, or for the Director to count as [idle](#the-end-of-a-turn) after `idle_after` with no turn signal.
`--now` applies it at the [end of the current turn](#the-end-of-a-turn); it is the last step of the handoff: write the note, then reload.
`--now` is refused with exit 1 unless the newest handoff note was recorded by the newest session, which is not `stopped`, and, while a Director stage attempt is running, unless the note was recorded after that attempt started, so a reload never drops an open grill.
The [status feed](#status)'s session section carries the newest request as `reload`, `requested` or `applied` with the old and new session numbers.

## The end of a turn

A stop or a reload `--now` ends the runtime at the end of the Director's current turn, not in the middle of it.
A runtime with turn hooks says when a turn starts and ends: Claude Code's `UserPromptSubmit` hook runs `guildhall director turn-started` and its `Stop` and `StopFailure` hooks `guildhall director turn-ended`, the second for a turn that ends in an API error, each of which finds the newest session from the copy in `GUILDHALL_COPY` and signals its supervisor over the session endpoint, an AF_UNIX socket at `supervisor.sock` in the session directory, bound as an [Artisan's endpoint](artisan.md#control-endpoint) is, on Windows as on Unix.
Each is exit 0 once the supervisor has answered and exit 1 otherwise, including for a usage error, because a `Stop` hook's exit 2 tells Claude Code to keep going and a `UserPromptSubmit` hook's exit 2 blocks the prompt.
The Director is idle while the newest of these signals is a turn end and no wake has typed into its pane since.
A turn the captain interrupts with Esc runs no hook at its end, so a runtime with hooks also counts as idle once no turn signal and no wake has arrived for [`[director] idle_after`](configuration.md#the-director-table), 5 minutes unless set; a turn that runs that long without a signal counts as idle too.
The supervisor decides at each signal and about once a second:

- `stop --now` ends the runtime at once;
- a stop or a reload `--now` ends it at the next turn end, or once 60 seconds have passed, when the runtime has no hook or the Director is idle;
- a plain reload ends it at a quiet point, or once 60 seconds have passed with no Director stage running when the runtime has no hook;
- a stop wins over a reload.

`director stop --now` is the escape when the runtime must end before then: it ends the runtime at once, mid-turn if need be.

The fake runtime has no hooks, so its sessions wait out the 60 seconds unless their script runs `turn-started` and `turn-ended` itself; without a turn-start hook a turn the captain starts is not seen, so once a turn has ended the Director counts as idle until a wake types into its pane.
A session directory whose socket path would pass the platform's AF_UNIX limit gets no endpoint; the supervisor says so when it starts and waits out the 60 seconds as for a runtime without a hook.

## Automatic handoff

```sh
guildhall director context-report [--copy <dir>]
```

Three triggers tell the Director a handoff is due before its context runs out ([decision](decisions/0085-director-handoff-triggers.md)); each is a [`[director]` setting](configuration.md#the-director-table) and 0 turns it off:

- `handoff_after_tasks` (off unless set): every that many tasks moved to `done` or `cancelled` since the session started;
- `handoff_context_percent` (70 unless set): the context the runtime last reported as used reaches the percent;
- `handoff_idle` (off unless set): the Director has had no turn signal and no wake for that long and, with turn hooks, its turn is over.

A trigger never ends the session itself: it asks the Director to run the [`guildhall-handoff`](#skills) skill, which writes a [note](#handoff), carrying any open grill questions, and then runs `director reload --now`.
The first two ride on a [wake](messaging.md#waking-the-director): when a wake types its line and a trigger holds, the line ends `; handoff due (<reason>)`, such as `guildhall: 2 unread (question 2); handoff due (context 72%)`, with two reasons joined by `, ` when both hold, so no model turn is spent on the reminder alone.
The idle trigger is the session supervisor's one timer: once the Director has been quiet for `handoff_idle` it types the same line, the unread counts with `; handoff due (idle <duration>)`, into the Director's pane.
Each crossing is announced once and recorded as a [handoff due](store.md#handoff-dues): the task trigger's crossings are every `handoff_after_tasks` finished tasks, so a Director that carries on is reminded again after as many more; the context trigger crosses once per session; the idle trigger once per quiet period, which a turn signal or a wake ends.
A wake that fails to type records no crossing, so the next one carries it.
The thresholds are recorded in the session when it starts, so a changed setting takes effect at the next session.

The context percent comes from the runtime, an optional capability (R1.24).
For Claude Code, the session's settings register `guildhall director context-report` as the status line: Claude Code runs it after each assistant message with the session's status as JSON on standard input, and it writes `context_window.used_percentage`, rounded, to `context.json` in the session directory, then prints `context <n>%`.
It finds the session from `GUILDHALL_COPY` and `GUILDHALL_SESSION`, which the supervisor sets for the runtime, and opens no store.
Input with no percent yet, before the first message, writes and prints nothing; input that is not JSON, or no session number, is exit 1.
A runtime without the capability, such as the fake, records no report path and its context trigger never fires.
The lab checklist proves the reporter against a real Claude Code.

## Guard rails

The guard rails are limits the binary enforces on a Director whatever it asks ([decision](decisions/0074-guard-rails.md)); `config.GuardRails` reads all three from the config.
In a standalone copy they are the copy's own config, which the Guildmaster edits; managed mode will make them read-only to the Director ([modes](director-modes.md#guard-rails)).

- Allowed repositories: the config's `[[repository]]` entries.
  `task new`, `task start`, `step done`, `step finish`, `step retry` and `messages send` refuse any other repository with exit 1, `guard rail repositories: the config does not list repository "<id>"`, before writing anything.
  `messages send` checks the envelope's repository when it has a config: the copy's, or one given with `--config`.
- Merge rule: each repository's `merge_policy`, which [deliver](interpreter.md#publish-and-deliver) enforces.
- Attempt caps: [`max_running_attempts`](configuration.md#settings), default 4, and `max_attempts_per_day`, default 40, across all the Director's repositories.
  An attempt runs from its start to its finish, and a day is a UTC day.
  The caps count attempts, not tokens or cost: each attempt's [spend](#spend) is recorded and shown beside them, but no budget refuses an attempt by its tokens or cost yet.

An advance whose next attempt would pass a cap, from `task start`, `step done`, `step finish`, `step retry` or a launcher's own advance, records and launches nothing and exits 1 naming the rail, such as `guard rail max_running_attempts: ... 4 attempts are running, the cap is 4`.
It also sends the Director a `budget-refused` message, once per task and UTC day.
The count and the start are one store write, so two advances never both take the last place, and an attempt already recorded is always launched again after a crash.
Nothing starts the refused attempt by itself: once an attempt finishes or the day turns, the Director runs `guildhall task start` for the task again, or `step retry` again for a refused retry.

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
- Guard rails: with `max_running_attempts = "1"`, a `task new` for an unlisted repository is refused, a second task's start is refused while the first runs, the Director is told once with `budget-refused`, and the start is admitted once the first attempt is stopped.
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

It reads the store in one read transaction, opened read-only and waiting up to 30 seconds for a writer's lock as every advance does, so it never writes, never creates a store and succeeds once a writer lets go.
It then reads files under the state directory: each task's newest `next.json` and the attempt directories of running attempts, which it classifies with [`reconcile.Classify`](artisan.md#reconcile) and the launcher identity probe; classifying may create an attempt's `attempt.lock`, as `artisan status` does.
It prints one JSON line and exits 0; a missing store is exit 1 naming `guildhall store init`, a store that cannot be read is exit 1, and a usage error, including a missing `--json`, is exit 2.
With a [copy](running.md#copies), `--copy <dir>` or `GUILDHALL_COPY` stands in for `--store`, `--state` and `--config`.

The document has these sections:

- `as_of`: labelled like the [task inspection reads](tasks.md#inspection).
  `partial` is always `true`; `excluded` names what the feed does not inspect: `worker files`, `transcripts` and `forge state`.
  `sequence` and `committed_at` are the newest mutation committed anywhere in the store when the read began; an empty store has sequence 0.
  Live attempt state is read after the store, so it can be newer than `sequence`.
- `session`: the newest Director session: its `state`, `number`, supervisor `identity`, `release`, `pane` when bound, `started_at`, `ready_at` and `ended_at` once set, `stop_now` for a `stop --now`, `sweeping` while a [stop](#stop) has not finished stopping its attempts, the `end_reason` of a session [stopped](#stop) as `superseded`, and `context`, the newest [context report](#automatic-handoff) with its `used_percent` and `reported_at`, absent when the runtime reported none.
  `last_wake` is the newest [wake](messaging.md#waking-the-director) of any message to the Director, with its `message_id`, `sender_type`, `decision`, `reason` when queued, `time` and `sequence`, so the feed says whether the last message woke anyone and why not; it is absent before any wake.
  A store with no session reports only its `state`, `"no session"`, `last_wake` and `guard_rails`.
  `guard_rails` are the [guard rails](#guard-rails): the config's `repositories`, `merge_policies`, `max_running_attempts` and `max_attempts_per_day`, when the copy has a config, and always `running_attempts`, `attempts_today` and the UTC `day` they count, and `spend_today`, the [spend total](#spend-totals) of the attempts started that day, read in the same store transaction.
  A config that exists but cannot be read gives `config_error` in place of the rails.
  The identity's liveness is not probed yet.
- `mode`: `{"name":"standalone"}` until managed mode lands.
- `quality`: the effective [quality settings](configuration.md#quality-settings), `self_review`, `size_budget_lines`, `size_budget_files`, `audit_sample`, `reference_runs`, `reference_runs_per_task`, `reference_cost_cap_usd` when set, `judge_separate_family` and `skill_edits_need_approval`, when the copy has a config; absent when it has none or its config cannot be read, which `guard_rails` reports as `config_error`.
- `tasks`: every task in repository then id order, with its `state` (`open`, `done` or `cancelled`), bound `workflow`, and:
  - `next`: the `next.json` the task's last advance wrote beside its task lock, at `<state>/<repo>/<task>/next.json`, copied as JSON exactly as written; absent when there is none.
    A file that is not JSON is reported as `next_error` instead.
    Every `task start`, `step done`, `step finish` and `step retry`, typed or run by the [watcher](running.md#the-watcher), writes it, so it is absent only for a task never advanced.
  - `steps`: for each step with an attempt, in the order the steps first started, its latest `attempt` with its `outcome`, or, while it runs, `live`: the classification `state` and `reason` from `reconcile.Classify`.
    A running attempt with neither an ownership record nor an attempt directory is `absent`, as `artisan status` reports it.
  - `spend`: the [spend total](#spend-totals) of the task's attempts.
- `decisions`: every open decision in repository, task and id order, with its `kind`, `subject`, `options`, `question` and `opened_at`, the commit time of its opening.
- `messages`: the Director's `unread` count, the count `by_type`, and the `headers` of at most the 20 oldest unread messages (`id`, `type`, `repository`, `task_id`, `step` and `attempt` when given, `sequence`).
- `recent`: the last 20 receipts of any task across repositories, in commit order, each with its operation, mutation id, sequence, commit time and record name, so records such as delivery's `publish-<k>-pr` documents and its `merged`, `merge-refused` and `workspace-kept` messages appear without a change to the feed.
- `needs_me`: what is waiting on the Director or the Guildmaster, each item with its `kind`, the task and attempt or decision it concerns, a `reason`, and the `command` the Director would run next, written without `--copy` because a Director session sets `GUILDHALL_COPY`.
  Items come in this order, and within a kind by repository, task, then step or message order:
  1. `advance`: an open task's step whose latest attempt is finished while the task's `next.json` still says `"wait":"attempt"` for that attempt, as a [watcher](running.md#the-watcher) whose advance failed leaves it; `guildhall step done --repo <r> --task <t> --step <s> --attempt <n> --result <file>` for a `succeeded` attempt whose result file exists, otherwise `guildhall task start --repo <r> <t>`.
     A [Director stage](interpreter.md#director-stages) whose attempt is unfinished but has a published result, as a `step finish` that stopped before its advance leaves it, is also an `advance` item, with that `step done` command, whether or not its stage message was read.
  2. `retry`, `retryable: true`: an open task's step whose latest attempt finished `failed` or `stopped`; `guildhall step retry --repo <r> --task <t> --step <s> --attempt <n>`, to which the Director adds `--reason`.
  3. `resume`, `resume: true`: a running attempt of an open task classified `stuck`, `exited` or `unknown`; `guildhall artisan resume --repo <r> --task <t> --step <s> --attempt <n>`.
  4. `stalled`: an unread `worker-stalled` message; `guildhall messages read <id>`.
  5. `decision`: an open decision; `guildhall decide show --repo <r> --task <t> --decision <id>`.
  6. `messages`: one item counting the other unread messages; `guildhall messages list director`.
  7. `director`: an open task whose `next.json` says `"wait":"director"`; `guildhall task timeline --repo <r> --json <t>`.
  8. `paused`: an open task whose `next.json` says `"wait":"paused"`, held while the Director was [paused](#pause-and-resume); `guildhall task start --repo <r> <t>`.
  9. `checks`: an open task whose `next.json` says `"wait":"checks"`, with its `step`, which the session supervisor's [checks watcher](#checks) advances; `guildhall task start --repo <r> <t>`.

  The newest Director session, while it is `recovering`, comes before all of them as a `recovering` item; `guildhall director ready`.
  Done and cancelled tasks contribute no `advance`, `retry`, `resume`, `director`, `paused` or `checks` item.

Times are commit times of the store's receipts, in UTC.
The feed reads every receipt in the store, so its cost grows with the store's mutation count, as `task timeline`'s does.

### Spend totals

A spend total sums the [spend](#spend) of every finished attempt, and of a running one that has recorded its spend: `attempts` counts them, `tokens_unknown` and `cost_unknown` count those whose tokens or cost are unknown, which add nothing, and `input_tokens`, `output_tokens`, `cache_creation_input_tokens`, `cache_read_input_tokens` and `cost_usd` are the sums.
`tokens` and `cost` are the sums' measures: `unknown` when no attempt's number is known, `estimated` when any summed number is, otherwise `measured`.
A task or day with no such attempt has `attempts` 0 and both measures `unknown`.
