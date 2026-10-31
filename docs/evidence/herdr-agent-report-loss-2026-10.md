# Why Herdr lost d23 build-4's agent report (October 2026)

Task d28 investigates one observation: on 2026-10-28 at about 22:10 UTC, Herdr showed d23 build-4's pane `w3H:p2` with no agent and `agent_status: unknown` while that attempt was still running.
At the same time, d8 build-8 (`w3E:p2`) and d22 build-5 (`w3F:p2`) still showed `agent: artisan, working`.
The running binary was `115b02e5`, from before d16's workspace close sweep.
These facts come from the d28 grill; they are not reproduced here.

d28 is an investigation only (grill decision "d28 - b").
It delivers this evidence and a recommendation; the fix is a follow-up task for the Director to open.

Every claim below cites a file and line at `ee8ee571`, a state or log path, or Herdr's help.
A claim that needs a source this investigation could not read is marked **needs state read** (readable without a lab, by a session allowed to read the copy's `state/`, `~/.claude/` and Herdr's help) or **needs the lab run** (needs a guarded `fm-lab-` Herdr session).

## Summary

**The cause is not established.**
Guildhall's report almost certainly succeeded, and something on Herdr's side, or inside the attempt, later cleared it.
Guildhall keeps no record of the report or of Herdr's answer, so its own files cannot say which.

The leading hypotheses, ranked:

1. **Something inside the attempt addressed its own pane through the inherited Herdr environment.**
   The worker inherits the pane's `HERDR_*` variables, and print-mode Claude runs the user's hooks and plugins.
   A Herdr-aware hook, or any bare `herdr` call made inside the attempt, would target `w3H:p2` itself.
   That fits the observed state: the agent was cleared, not replaced by another.
   It does not yet explain why d8 and d22 kept their reports.
2. **Herdr's own agent detection or an activity-tied expiry overrode the source report.**
   The pane's foreground process is the `guildhall` bridge, not `claude`, and Guildhall reports only once.
   If Herdr re-evaluates the pane on output or foreground changes, a source report could be dropped.
3. **A Herdr-side race when the foreground changed from the shell to the bridge**, just after the report.
   The same sequence ran for all three panes, so it would be timing-dependent.

The cheapest decisive step is the state reads listed under [Sources](#sources), which this build's sandbox refused.
If they do not settle it, the lab run in the d28 spec does.

The recommended fix is a ranked shortlist; see [Recommended fix](#recommended-fix).
Its first item, recording the report's outcome as an event, is worth doing whichever hypothesis holds.

## How the report is made today

The sequence is `launch.(*Process).launchPane` ([`internal/launch/presentation.go:76-138`](../../internal/launch/presentation.go)):

1. It writes the single-use claim `pane-launch.json` as `{"state":"launching"}` (`presentation.go:86-97`).
2. It creates the workspace labelled `└ <task> · <step> #<n>` (`presentation.go:99`).
   In production that is `artisanWorkspaces.CreateWorkspace` ([`internal/cli/artisan_order.go:45-71`](../../internal/cli/artisan_order.go)), which creates the workspace with root pane `…:p1` and, under the workspace-order lock, journals it and moves it below the Director (`artisan_order.go:97-156`).
3. `TabLauncher.Open` creates a second tab (`adapter.HerdrTabs.CreateTab`, [`internal/adapter/run.go:156-177`](../../internal/adapter/run.go)).
   It waits for the starting shell, closes the starting tab, and waits until the new pane's shell is idle (`run.go:463-515`).
   This is why every Artisan pane id ends in `:p2`.
4. It rewrites `pane-launch.json` with the tab binding (`presentation.go:108-114`).
5. **The agent report**: `presentation.Report(ctx, tab.PaneID, AgentReport{Source: "guildhall", Agent: "artisan", State: "working", Resume: ResumeArgv(at)})` (`presentation.go:115-117`).
   In production `Report` is `herdr.Client.ReportAgent` ([`internal/cli/artisan_presentation.go:34`](../../internal/cli/artisan_presentation.go)).
6. Only then does it type `guildhall artisan start … --pane-launch --workspace-close-after <d>` into the idle shell (`presentation.go:118-124`, `run.go:478-487`).

`herdr.Client.ReportAgent` ([`internal/herdr/herdr.go:675-699`](../../internal/herdr/herdr.go)) validates its inputs and runs `herdr pane report-agent --source guildhall --agent artisan --state working <pane> --session <s> -- <resume argv>` (`herdr.go:697`).
It goes through `callTail` (`herdr.go:144-169`), which takes **exit 0 as success and discards stdout**.
Nothing checks the response body, and nothing reads the agent back.

The report is made **once** in an attempt's life.
`git grep ReportAgent` finds one production caller, `internal/cli/artisan_presentation.go:34`: there is no refresh, no change to `idle` or `blocked`, and nothing at finish.
**No event or log line records it**: `launchPane` writes nothing to `events.jsonl` or `launcher.log` about the report or Herdr's answer.

`ResumeArgv` ([`internal/launch/resume.go:225-237`](../../internal/launch/resume.go)) is `guildhall artisan resume --store … --state … --repo … --task … --step … --attempt …`.
It starts with a plain command name because Herdr refuses a path (`herdr.go:658-661`, `herdr.go:692-694`, [`docs/decisions/artisan-resume.md:10`](../decisions/artisan-resume.md)).

### What runs in the pane after the report

- The pane's foreground process becomes the bridge `guildhall artisan start --pane-launch`, which calls `launch.Present(argv, os.Environ(), …)` ([`internal/cli/artisan.go:279-288`](../../internal/cli/artisan.go)).
  `Present` starts the owned launcher detached and streams `launcher.log` into the pane (`presentation.go:145-180`).
- The launcher starts the worker with `append(os.Environ(), c.Command.Env...)` ([`internal/launch/launch.go:345`](../../internal/launch/launch.go)).
  So the bridge, the launcher, the Claude worker and everything the worker runs **inherit the pane's environment**, including any `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`, `HERDR_SESSION` or `HERDR_ENV` that Herdr sets in its panes.
- Guildhall strips those variables only from its own `herdr` calls (`herdr.go:48-50`, `herdr.go:171-186`).
  `git grep HERDR_ -- '*.go' ':!*_test.go'` finds no other stripping.
- The Claude worker runs in ordinary print mode, which "discovers hooks, skills, commands, plugins, agents and MCP servers" ([`docs/adapters.md:190`](../adapters.md)).
  The Artisan settings file passed with `--settings` ([`internal/adapter/claude/claude.go:290`](../../internal/adapter/claude/claude.go)) adds allow rules; hooks from the user's and the repository's settings still run.
- The repository's own scripts and plugin files never call Herdr: `git grep -il herdr -- '*.js' '*.cjs' '*.mjs' '*.json' '*.sh'` finds only a test fixture.

## Q1: did the report for `w3H:p2` succeed, and what dropped it?

**It succeeded, by inference from the code.**
A failed report returns an error before the launch command is typed (`presentation.go:115-117`), so no launcher would have started.
d23 build-4 was running, so `herdr pane report-agent` exited 0.
Guildhall saved nothing of Herdr's answer, so no log confirms it.
Confirming it from the attempt's files **needs state read** of `state/guildhall/d23/build-4/launcher.log` and `events.jsonl`, though from the code above they are not expected to mention the report.

**What dropped it is not established.**

| Candidate | Evidence | Verdict |
|---|---|---|
| Activity-tied expiry: the report is sent once and Herdr expires it | d23 build-4 (`w3H`) was the **newest** of the three panes; `w3E` and `w3F` were older and kept their reports (grill facts). A plain time-to-live from the report would have dropped the older ones first. | Not a plain TTL. A TTL reset by activity, such as output or foreground changes, is not ruled out. **Needs state read** of `herdr pane report-agent --help`, then **needs the lab run**. |
| Foreground-process change after the report: shell → `guildhall` bridge, plus the detached launcher | The same sequence (`presentation.go:115-119`) ran for all three panes, and two kept their report. | Not a deterministic cause. Possible as a Herdr-side race if it re-evaluates the agent on a foreground change close to the report. **Needs the lab run.** |
| Herdr's own agent detection overriding the source report | The pane's foreground is `guildhall`, not `claude`; the Claude worker is a detached descendant (`presentation.go:150-155`). Nothing in the repository says how detection treats a source report. | Unknown. **Needs state read** of `herdr pane report-agent --help` and `herdr pane get --help`, then **needs the lab run**. |
| An inherited `HERDR_*` environment inside the worker | The worker inherits the pane's environment (`launch.go:345`, `cli/artisan.go:285`), and print-mode Claude runs the user's hooks (`docs/adapters.md:190`). A Herdr-aware hook, or any `herdr` call the Artisan or its tests make without an explicit `--session` and pane, would target `w3H:p2` itself. That matches the observed state: no agent and `unknown`, rather than another agent. d23's own change, task priority (`git log guildhall/d23`), does not touch Herdr. | **Leading hypothesis.** It does not yet explain why d8 and d22 survived; the timing of a hook event, such as `Stop` or `SessionEnd` of a sub-session, might. **Needs state read** of `~/.claude/settings.json` and installed plugins' hooks for `herdr` calls, and of d23 build-4's `launcher.log` for `herdr` invocations; then **needs the lab run**. |
| Nearby workspace closes | d16's sweep was not running (binary `115b02e5`). A hand close of `w3G` (d23 build-3) or another workspace uses `workspace close <id>`. Pane ids are workspace-scoped, and nothing in Guildhall closes tabs or panes inside `w3H` after launch. | Possible only if Herdr re-binds agent records on a close. **Needs the lab run.** |
| Id reuse: `w3H`'s record belonged to an earlier, closed `w3H` | See [Q4](#q4-does-herdr-reuse-or-renumber-workspace-ids). No Herdr restart is recorded, and the ids look monotonic within one server run. | Unlikely without a restart. |
| The workspace move (`workspace.move`, `artisan_order.go:63`, `herdr.PlaceWorkspace`) | The move happens inside `CreateWorkspace`, before the tab is created and before the report (`presentation.go:99-117`). d23 build-4 was the latest child, so no later sibling's move touched it. | Not a cause through ordering. |

## Q2: was the resume command lost too, and what does that mean for recovery?

- The agent and the resume command are registered by the same `report-agent` call (`presentation.go:115`, `herdr.go:697`).
  Whether Herdr keeps `resume_argv` when the agent record goes is unknown from the code.
  The recorded Herdr 0.9.5 pane responses show `agent_status` and no resume field ([`internal/herdr/testdata/workspace_create.json:4-19`](../../internal/herdr/testdata/workspace_create.json)), and Guildhall never reads it back.
  Whether `pane get` shows `resume_argv` **needs state read** of `herdr pane get --help`; treat it as lost until Herdr's output or the lab run shows otherwise.
- **The effect on crash recovery is small.**
  The resume command only reconciles and never starts a worker ([`docs/artisan.md:424-448`](../artisan.md#resume)).
  The launcher is detached from the pane, and losing Herdr cannot signal it (`docs/artisan.md:466-469`).
  The Director's status feed lists every `stuck`, `exited` or `unknown` attempt as a `resume` item with the exact `guildhall artisan resume …` command ([`docs/director.md:457`](../director.md), [`internal/cli/director_status.go:393`](../../internal/cli/director_status.go)).
  So a lost `resume_argv` loses only Herdr's automatic resume after a Herdr restart; the attempt is still settled when the Director works its status feed or someone runs `artisan resume` by hand.
- The lost label's real cost is **presentation**: the Guildmaster and Herdr's sidebar cannot see that the pane holds a working Artisan.

## Q3: how often does it happen?

- **Guildhall's records cannot answer it.**
  The report leaves no trace in `pane-launch.json`, which holds only the tab binding (`presentation.go:108-114`), nor in `launcher.log` or `events.jsonl`.
  The only evidence is live Herdr state for workspaces still open.
  Which attempts lost their label, and what they share, **needs state read**: every open attempt's `pane-launch.json` joined against `herdr pane list` or `pane get` in the Director's session.
- **The evidence is disappearing.**
  d16's close sweep, merged in `c12e3682`, closes a finished attempt's workspace `artisan_workspace_close_after` (default 5 minutes) after its finish (`docs/artisan.md:480-488`).
  Once a build with d16 is installed, only running attempts can be surveyed.

## Q4: does Herdr reuse or renumber workspace ids?

- The repository asserts that "Herdr ids are short ordinals that a restart may reuse" ([`docs/decisions/artisan-herdr-workspaces.md:31`](../decisions/artisan-herdr-workspaces.md)) without a source.
- The recorded ids point to **one counter per server run, never reused within it**: d23 used `w2Y`, `w3A`, `w3G` and `w3H`, while d8 build-8 had `w3E` and d22 build-5 had `w3F`.
  These ids come from the d28 grill and the research notes, not a primary record; confirming them **needs state read** of each attempt's `pane-launch.json` and of `state/artisan-workspaces.json`.
  Other tasks' workspaces fill the gaps, and `w2Y` → `w3A` suggests a counter whose last place runs over letters.
  The 0.9.5 fixtures start at `w1` (`workspace_create.json:9,18`).
- Whether a **restart** reuses ids is not covered by anything here and **needs the lab run**.
- Guildhall already guards against reuse twice: the presentation journal refuses a workspace id it has already journalled ("workspace already journalled; refusing retry", `artisan_order.go:138-140`), and the close sweep revalidates the whole workspace, not the id (`docs/artisan.md:483`).
  A restart that reused ids would therefore show as that warning on the launching process's stderr (`artisan_order.go:68`), and as duplicate ids in `state/artisan-workspaces.json`; that read **needs state read**.

## Recommended fix

The cause is not established, so this is a ranked shortlist.
Each option says which evidence would make it the one to build.

1. **Record the report's outcome as an event.**
   After `presentation.Report` returns, append an `events.jsonl` event with the pane id, Herdr's exit status and stdout, and, once, the agent read back with `pane get`.
   Build this whichever hypothesis holds: without it, no future loss can be diagnosed from Guildhall's files (Q1, Q3).
   A read-back or event failure must only warn.
   Built in d30 as the [`presentation-report` event](../artisan.md#events).
2. **Strip `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`, `HERDR_SESSION` and `HERDR_ENV` from the worker's environment.**
   Strip them where the launcher builds the worker's environment (`launch.go:345`), ideally with the same list `herdr.Client` already strips (`herdr.go:50`, which also holds `HERDR_TAB_ID` and `HERDR_WORKSPACE_ID`), so nothing inside an Artisan can address its own presentation pane.
   Build this first if the state read finds a `herdr` call in `~/.claude/settings.json`, an installed plugin's hooks, or d23 build-4's `launcher.log`.
   The advance watcher and the Director wake do not depend on these variables: they make their own `herdr.Client` with an explicit session and strip ambient variables anyway (`herdr.go:50`, [`internal/cli/wake.go:37-47`](../../internal/cli/wake.go)).
3. **Re-report periodically, so a lost label heals.**
   Repeat the same `report-agent` call, for example from the launcher's heartbeat or on each close-sweep pass, while the attempt is owned.
   Build this if Herdr's help or the lab run shows that detection, activity or a foreground change clears source reports.
   It needs the pane binding from `pane-launch.json` and the Director's session.

Any fix must keep these constraints:

- **Never fail a launch over presentation.** Ordering already never fails an Artisan launch (`docs/artisan.md:462`); an event, read-back or re-report failure must only warn.
- **Never type a second command or launch twice.** An ambiguous response leaves the claim and types nothing more (`presentation.go:119-124`, `docs/artisan.md:474-478`); a re-report is a `report-agent` call, never typed input.
- **Never treat a pane or label as liveness evidence.** The terminal is a presentation binding only (`presentation.go:20-26`, [`docs/decisions/artisan-herdr-workspaces.md:5`](../decisions/artisan-herdr-workspaces.md)); a missing label must not change an attempt's classification, and a re-report must not imply the worker is alive.

The tests that pin today's behaviour are `TestReportAgentRegistersTheResumeCommand`, `TestReportAgentRefusesWhatHerdrWouldReject` and `TestReportAgentReturnsHerdrsRefusal` ([`internal/herdr/herdr_test.go:493-548`](../../internal/herdr/herdr_test.go)), and [`internal/launch/presentation_unix_test.go`](../../internal/launch/presentation_unix_test.go).

## d49 finding: Herdr drops a report made at an idle shell

d45 build-1 (`w3S:p2`) and d46 build-1 (`w3W:p2`) recorded `presentation-report` events whose read-back, taken just after the launcher owned the attempt, showed `agent_status: unknown` and no `agent`, while d48 research-1 (`w3Z:p2`) read back `agent: artisan, working`.
The d49 research reproduced this on Herdr 0.9.5 in an isolated `fm-lab-d49` session, with `sleep` standing in for the bridge; every report exited 0 with no output:

| Pane foreground at the report | What followed | Read back |
|---|---|---|
| idle shell | nothing | shows the agent at once, then `unknown` with no agent on Herdr's next tick (200 ms to 1 s): **lost** |
| idle shell | a foreground process at once | the agent, still held while that process runs: **held** (the race won, as d48) |
| idle shell | a foreground process 1.5 s later | `unknown`; the later process does not bring it back: **lost** (as d45, d46) |
| a non-shell process | 2 s, then 5 s more | the agent both times: **held**, until that process leaves the foreground |

Guildhall made its only report at the idle shell, before typing the bridge, so whether it held was a race between Herdr's tick and the typed bridge reaching the foreground.
Herdr accepts the report and drops it on its own side; that matches its documented rule that an agent identity "follows the current pane occupant", and an idle shell is not an occupant.

Verdict on the hypotheses above:

- **#3, a race when the foreground changes from the shell to the bridge: confirmed, with a correction.** The change does not drop the report: a report at an idle shell is dropped *unless* the change comes quickly enough.
- **#2, Herdr's detection overriding the report: confirmed in part.** Herdr clears a report with no non-shell occupant; a report bound to a running process was not overridden.
- **#1, an inherited `HERDR_*` call inside the attempt: not needed** to explain d45 and d46, whose read-backs came moments after ownership.

The fix (d49) keeps the first report and, once the launcher owns the attempt and the bridge is in the foreground, repeats the same report until a read-back shows `agent: artisan` with a known `agent_status`: up to 5 read-backs, 300 ms apart, within 3 s, warning on stderr when it never does ([`presentation-report`](../artisan.md#events) records `reports`, `readbacks`, `readback_agent` and `retry_error`).
The window is Herdr-internal and undocumented; the repeat does not depend on it.
The regression test is `TestLostReportIsRepeatedUntilThePaneShowsIt` ([`internal/launch/presentation_report_test.go`](../../internal/launch/presentation_report_test.go)); its failure before the fix is in [`herdr-agent-report-retry-2026-10-30-red.txt`](herdr-agent-report-retry-2026-10-30-red.txt).

## Sources

Code and docs were re-checked at `ee8ee571`, the `main` commit this branch is rebased onto; the line numbers above are from that revision (unchanged from `78323ce2`, since `ee8ee571` only adds `docs/evidence/github-merge-queue-2026-10.md`).

The d28 build attempt (build-1, builder profile) attempted each state read in the spec on 2026-10-28; the sandbox refused all of them:

| Read | Answers | Result |
|---|---|---|
| `~/.claude/settings.json` and installed plugins' hooks, for `herdr` calls | Q1, inherited environment | Refused: read permission not granted. **Needs state read.** |
| `state/guildhall/d23/build-4/launcher.log` and `events.jsonl`, for `herdr` invocations | Q1 | Refused: read permission not granted. **Needs state read.** |
| Every open attempt's `pane-launch.json`, joined against `herdr pane list` / `pane get` in the Director's session | Q3 | Refused: `state/` cannot be listed, and `herdr` needs approval. **Needs state read.** |
| `state/artisan-workspaces.json`, for duplicate workspace ids | Q4 | Refused: read permission not granted. **Needs state read.** |
| `herdr pane report-agent --help` and `herdr pane get --help` | Q1, Q2 | Refused: the command needs approval. **Needs state read.** |

The lab run was not done: it needs a temporary `[profile.researcher]` grant of `Bash(herdr:*)` and `Bash(go test:*)` that was not in place, so there is no lab transcript.
Its steps, from the d28 spec, are: report an agent on a pane in a guarded `fm-lab-` session; then exec a new foreground process, run a `claude` descendant, close neighbouring workspaces, and call `herdr` with the inherited `HERDR_PANE_ID`, reading the agent back after each step; and restart the lab server once, recording the workspace ids before and after.
