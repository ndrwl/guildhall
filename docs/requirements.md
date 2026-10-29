# Guildhall requirements

This is the accepted product specification for Guildhall.
Requirement ids are stable: reviews and pull requests cite them, and a changed requirement keeps its id.
Text marked *(default)* is an engineering default that may change during implementation without a product decision; everything else is an accepted requirement.
A requirement describes intended behaviour, not shipped behaviour: see the README for what exists today.

## Glossary

- **Guildmaster**: the human user who owns the guild.
- **Steward**: the shared maintenance agent.
- **Director**: the coordinator for one or more repositories on its one owner machine.
- **Artisan**: a task worker launched by a Director.
- **Herdr**: the terminal multiplexer whose workspaces, tabs and panes present Directors and Artisans.

## Product and repositories

| # | Requirement |
|---|---|
| R1.1 | Guildhall is a public MIT-licensed repository started fresh. Code reused from other MIT projects keeps its licence notices |
| R1.2 | The public repository contains no personal preferences, private learnings, machine or project roster, or credentials |
| R1.3 | A separate private repository holds central preferences, learnings and the roster; configuration names it |

## Roles

| # | Requirement |
|---|---|
| R1.4 | The roles are Guildmaster, Steward, Director and Artisan. A Director coordinates one or more repositories on its one owner machine. There is no per-machine role |
| R1.5 | Directors only orchestrate and never do the work themselves; Artisans do all the work |
| R1.6 | The Guildmaster talks directly to each Director; there is no central dispatcher |
| R1.7 | The Steward maintains tools, central preferences and learnings, and performs rollouts, audits and provisioning, including installing new tools on named machines. It keeps Director-filed maintenance tickets with evidence in the private configuration repository for Guildmaster triage; standalone Directors keep the same queue locally until a Steward exists. Filing or collecting a ticket authorizes no maintenance execution. It acts only on request, with no periodic model spend. Each install is previewed and runs only on an explicit Guildmaster request for that machine |
| R1.8 | The Steward is reached over SSH and pushes configuration to Director copies over SSH during rollouts; Director machines need no access to the private repository |

## Configuration

| # | Requirement |
|---|---|
| R1.9 | Each Director copy has its own configuration: a `mode` (steward or director), a private-configuration reference (Steward only), a `managed` section written only by Steward pushes, a `local` section owned by the Director, and a repository list (path, remote, owner machine, merge requirements). Each managed key says whether a local override is allowed *(default: the exact layout)* |
| R1.10 | No configuration contains credentials; sign-in stays in each runtime's own store |
| R1.11 | Workflows are JSON (R1.56). The rest of the copy configuration is TOML *(default)*. Every schema is versioned, and an unknown newer schema version is refused rather than guessed |

## Workflow and state

| # | Requirement |
|---|---|
| R1.12 | Each task uses the workflow selected for its repository (R1.56) as its normal route, with recorded Director-directed alternatives under R1.58. Guildhall ships a default workflow: grill and research, plan and specification, Guildmaster acceptance, build, independent review against the accepted specification and central standards, bounded fixes, and delivery under the merge requirements that repository chose (R1.36) |
| R1.13 | Grilling answers, research and design documents are stored durably in the project store and referenced from task records; a restart never depends on a runtime transcript |
| R1.14 | Within a Director, tasks form a dependency graph; independent tasks run in parallel; waits and routine step advances spend no model tokens (to be measured in acceptance) *(implemented: dependent tasks wait at the [dependency gate](interpreter.md#the-dependency-gate) and the session supervisor's [dependency watcher](director.md#dependencies) advances them)* |
| R1.15 | Across Directors, work moves only by manual immutable handoff (`guildhall handoff export` and `import`), with no automatic cross-Director scheduling. Dependencies between repositories of one Director use that Director's own graph with no manual handoff |
| R1.16 | Each Director has one owner machine, where all its repositories live; it has no cross-machine responsibility and never executes tasks remotely. A machine may host several Directors. Changes apply to new tasks first |
| R1.17 | Each Director copy has one store owned by the tool, with records keyed by repository; large documents are referenced files; each task works in an isolated copy of its repository *(default: a single-writer bbolt database with mutation ids and receipts)* |
| R1.18 | *(default)* Unlanded work is never deleted; cleanup happens only after a landed-work check; the tool never calls a runtime's own cleanup command on unlanded copies |
| R1.19 | The default workflow uses one fresh review Artisan per review and up to three fix rounds before asking the Guildmaster *(default: the counts)*; both are workflow settings (R1.58) |
| R1.20 | Waiting decisions show on the Guildmaster's next message to that Director; `guildhall decide list` lists them per machine; there is no push service |

## Runtimes

| # | Requirement |
|---|---|
| R1.21 | The core is agent-agnostic: the Go tool owns all state and lifecycle; workflow, role and Artisan definitions are runtime-neutral files rendered per runtime |
| R1.22 | Claude Code is the first runtime adapter; Pi and Codex are future adapters. A fake adapter with no optional capabilities runs in CI to keep the core runtime-neutral *(tests select the fake adapter through explicit profiles; profile-less Artisan steps are refused)* |
| R1.23 | Baseline adapter contract: the tool launches the agent's normal interactive session in a Herdr pane it creates, with a prompt file and working directory; the Artisan writes a result file that the tool validates, and completion comes from that file, not from process exit or the terminal; the tool can stop the Artisan. Herdr's agent-state signals are optional hints only |
| R1.24 | Optional adapter capabilities are used only once proven: pre-assigned session id and resume, native background sessions, hooks, structured output and remote control |
| R1.25 | Remote control of Directors is an optional capability subject to proof. Baseline access is a Herdr tab per Director (R1.37), reached remotely through Herdr's saved SSH machine profiles; plain SSH remains the fallback |
| R1.26 | An optional Claude plugin shows a read-only status band and reply visuals by calling `guildhall`; it is never state or lifecycle authority |
| R1.27 | A runtime or platform is described as supported only after its adapter passes acceptance there |
| R1.28 | Each machine signs into the Guildmaster's own Claude account; Guildhall stores nothing |
| R1.29 | There is no always-on Guildhall service: the tool runs a small deterministic watcher per running task (to be proven in acceptance). The Herdr server on each Director machine is the terminal host, not Guildhall state |

## Platforms, rollout and delivery

| # | Requirement |
|---|---|
| R1.30 | Guildhall runs natively on Windows, Linux and macOS as a single Go binary per platform, with CI on all three for every change. Live acceptance runs on a Linux pilot (WSL) and a native Windows pilot machine, and on a Mac once one is named. CI-only macOS is not claimed as full live compatibility |
| R1.31 | A rollout updates the release pin and managed preferences on named copies only; only new tasks pick them up; active tasks keep their recorded release; rollback restores the previous default for new tasks; releases are installed side by side *(default: side-by-side layout)* |
| R1.32 | Learnings: a Director proposes with evidence and limits, the Steward drafts the change, the Guildmaster approves, and it ships as a versioned change, never as raw replication |
| R1.33 | Linked plans and reviews are always plain Markdown or HTML files; a richer viewer may be used when installed *(default)* |
| R1.34 | Work is not capped by an attempt budget: it continues until done, with no test or process waiver |
| R1.35 | The first implementation step probes the baseline adapter contract, Herdr tabs and each optional Claude capability on the Linux and native Windows pilots in an isolated Herdr lab session; macOS follows once a Mac is named *(default: the probe order)* |
| R1.36 | Each repository chooses its merge requirements: the Guildmaster's merge answer, the independent review Artisan's approval of the exact head against the accepted specification and standards, passed required CI, and nothing open (no findings and no unanswered decision). Every requirement is on by default, so the Guildmaster approves every merge. A repository that turns the Guildmaster's answer off is merged by Guildhall once every other requirement holds, with a recorded authorization; otherwise the Guildmaster is asked. A required review approval or CI pass is never merged over, by anyone; the Guildmaster's merge answer overrides only nothing open ([merge requirements](decisions/merge-requires.md)). A head Guildhall rebased onto the base is covered by its recorded rebase, either tier-1 patch equivalence or a clean narrowed review of the resolved hunks, together with the approval of the head it replaced; the earlier merge approval carries over to it only under the repository's `reapprove_after_rebase` ([merge queue and tiered rebase](decisions/merge-queue-rebase.md)) |
| R1.37 | Every Director is accessible in Herdr: on its owner machine, each Director has its own Herdr workspace, labelled with the Director's name, whose first tab is the Director's interactive session |
| R1.38 | Artisans are visible in Herdr: each running workflow Artisan gets its own workspace in its Director's configured Herdr session, labelled `└ <task> · <step> #<attempt>` and best-effort placed as a contiguous launch-order child block directly under its exact Director workspace (unsupported or ambiguous ordering warns and never fails launch), launched by the tool through the Herdr API without taking focus, with a plain readable live stream of assistant text, one short line per tool call and final result text. The tool, not Herdr, owns the Artisan's task record, isolated copy and lifecycle |
| R1.39 | A Guildmaster-run setup helper (`guildhall herdr setup`) connects the Guildmaster's Herdr client to every enrolled machine: it reads the roster, prints a dry-run plan of the Herdr machine profiles it would add (label, SSH target, remote session), applies only after confirmation, verifies each machine's Director tabs read-only, and reports missing or incompatible machines |
| R1.40 | The setup helper never performs SSH authentication for the user, never opens or exposes ports, never stops or replaces a remote Herdr server without explicit consent, and only touches machines enrolled by the Steward |
| R1.41 | A Guildhall release pins a Herdr version that supports machine API forwarding on both ends *(default: where the pin lives)*; installing or updating Herdr on a machine is a Steward tool-install action on explicit request (R1.7) |
| R1.42 | There is no unified cross-host workspace: the Guildmaster switches machine inside Herdr to reach that machine's Directors and Artisans |
| R1.43 | The Steward owns installation: an interactive, step-by-step setup wizard (`guildhall setup`) guides the Guildmaster through machine setup on native Windows, Linux and macOS |
| R1.44 | The wizard detects OS, architecture and prerequisites (Git, SSH server reachability, Claude Code, Herdr, Guildhall version, sign-in state) and previews the exact actions and versions for that OS before doing anything |
| R1.45 | A side-effect-free dry run is mandatory; applying requires the plan from that dry run and stops if the machine changed since |
| R1.46 | Enrolment is by name: the machine's label, its roles (Steward host and/or Director host) and its Directors and repositories are recorded in the private roster via the Steward |
| R1.47 | Manual steps, such as Claude sign-in, forge sign-in and administrator actions, are shown as exact commands and links that are printed, never opened automatically; the wizard waits for the Guildmaster, then verifies |
| R1.48 | Each setup step is check, act, verify; idempotent and resumable; progress lives in the machine's local untracked Guildhall state; secrets are never written to any configuration |
| R1.49 | Setup finishes with a verified summary: installed versions against the release pin, roles, Directors and their Herdr tabs, reachability from the Guildmaster's Herdr client (R1.39), and any manual items still outstanding |
| R1.50 | Setup uses existing installers only: the OS package manager where present, otherwise each tool's own vendor installer; there is no new package manager. The wizard grants no standing permission to install anything |
| R1.51 | *(default)* The Steward drives the wizard over SSH where SSH already works; a brand-new machine runs the same wizard locally first, bootstrapped from a downloaded, checksum-verified release binary whose link is printed |
| R1.52 | Ownership is independent of terminal lifetime: the tool records each Director or Artisan pane only as a presentation binding. Closing a tab or workspace, a Herdr restart or a reboot never changes task state, never loses the isolated copy or unlanded work, and never by itself counts as the Artisan finishing or dying |
| R1.53 | On a missing or ambiguous pane, the tool reconciles before acting: a result file means finished; otherwise it checks the recorded process and refuses a duplicate launch while liveness is unknown; it may re-open a workspace for an Artisan whose task is still open |
| R1.54 | The tool closes an Artisan's workspace only after its result is recorded and its work is landed or preserved; it never closes tabs, workspaces or sessions it did not create |
| R1.55 | Acceptance runs in an isolated, non-default Herdr lab session on the Linux and native Windows pilots, then a Mac once named: Director tabs and Artisan workspaces appear as specified; closing an Artisan workspace, restarting Herdr and rebooting leave task state and work intact; ambiguous state causes no duplicate launch; the fake adapter runs in a pane too; switching machines reaches remote Directors |

## Workflows

| # | Requirement |
|---|---|
| R1.56 | Each Director has JSON workflow definitions in its own configuration area, selected per repository with per-repository overrides |
| R1.57 | Guildhall ships a step registry of tested, versioned built-in step types (see the authoritative [built-in registry](../internal/workflow/registry.go)); workflow JSON names step types and gives their settings but cannot contain or invent executable code, shell commands or new step types |
| R1.58 | Workflows are rails, not tracks: an ordered list and bounded rules define the normal route for one task, not its only possible route. Each workflow and step kind carries its purpose. The Director may send work back to any earlier step with instructions, re-run, add a built-in step, split the task, hold, abandon or ask the Guildmaster, while the engine preserves the accepted specification, independent review of the final code, required validation, merge authorization under the repository's merge requirements and full records (R1.63). Changing the head invalidates stale evidence unless a recorded rebase proves coverage (R1.36), and merge approval carries over to a rebased head only under the repository's `reapprove_after_rebase`; the Director can grant neither ([merge queue and tiered rebase](decisions/merge-queue-rebase.md)). Unexpected states and refusals reach the Director with facts and safe available tools, never a silent wedge; edge cases and workarounds also produce evidence-backed maintenance tickets (R1.7). This does not introduce executable workflow code or replace the task dependency graph: independent tasks run in parallel and dependent ones wait (R1.14; "dependent ones wait" is implemented by the [dependency gate](interpreter.md#the-dependency-gate)). See [rails, not tracks](decisions/rails-not-tracks.md) |
| R1.59 | A workflow is validated before any task starts: its step types exist in the pinned release, inputs and outputs are wired and typed, loops are bounded, and the runtime adapter has the capabilities each step needs. An invalid workflow refuses to start and names the problem (`guildhall workflow validate`) |
| R1.60 | Human and automated review are interchangeable actors with one shared [review outcome contract](configuration.md#review-outcome) for code and document review |
| R1.61 | Human steps are durable pauses: the task waits with no model calls; the Guildmaster answers through the Director or `guildhall`, and the wait is listed in the per-machine inbox (R1.20); the exact answer is recorded; the task resumes deterministically, including after a restart |
| R1.62 | Each task binds an immutable base workflow revision (content digest) when accepted; later definition edits apply to new tasks only, unless the Guildmaster explicitly migrates a given task. Director route changes under R1.58 are recorded alongside that revision with reasons, inputs and evidence invalidation, never by rewriting the definition or prior attempts |
| R1.63 | Workflow choice, approval and route changes never waive the accepted-specification binding, independent non-author review of the final code, required validation, the merge requirements the repository chose (R1.36), or full durable records and custody. A required review approval or CI pass is never merged over; the Guildmaster's merge answer overrides only nothing open, and only a repository's own configuration turns a requirement off. Changed scope needs renewed Guildmaster acceptance; changed evidence cannot reuse stale review, validation or merge approval, unless a recorded rebase proves that a rebased head changes nothing the review approved (R1.36); merge approval carries over to a rebased head only under the repository's `reapprove_after_rebase`, and the Director can grant neither. The Director cannot skip review, approve on the Guildmaster's behalf or merge without the authorization the repository's merge requirements demand. Human review may fully replace automated review on a repository |
| R1.64 | Acceptance: every built-in step type is tested on its own; the default workflow runs end to end; swapping automated for human review on one repository leaves others unchanged; invalid workflows refuse; a human pause survives restart with zero idle model calls; in-flight tasks keep their workflow revision after an edit |

## Open questions

- Whether the structural [review floor](configuration.md#enforcement-at-task-creation) is set per repository through a managed policy key is not yet decided.
  Current admission permits a Guildmaster-approved revision below that floor; the target invariant in R1.63 is not waivable by such approval, and enforcement independent of workflow shape remains follow-up work.
  Workflow-edit approval follows the [workflow-edit authority policy](configuration.md#workflow-edit-authority).
