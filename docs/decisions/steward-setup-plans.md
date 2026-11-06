---
status: accepted
date: 2026-11-04
topic: steward
tasks: d128
amends: steward-fixed-remote-commands.md
---

# A Steward changes a machine only through the steps of a setup plan the Guildmaster approved

## Context

The Guildmaster asked for the Steward to own a Director's whole setup: taking over an existing Director, such as the laptop's WSL copy, and later provisioning new ones (d128's grill).
Until now the Steward could only copy `managed.toml` with `scp` and run the [fixed remote commands](steward-fixed-remote-commands.md), and its role forbade it from creating a copy or writing a copy's files.
Bringing a copy under the Steward was five steps by hand on the Director's machine ([managing a Director copy](../steward.md#managing-a-director-copy)).
The grill also asked that the Steward, an agent, diagnose a failed setup and retry it, while a fix outside the plan needs the Guildmaster's word.

## Decision

Every change a Steward makes to a machine is a step of a **setup plan** (`guildhall.setup-plan/1`), a plan kind beside the rollout plan, made by a dry run and applied only with `--approve <its exact digest>`.
The Guildmaster approves it through the reserved task `steward/setup`, whose decisions a session can never answer and whose `--approve` a session can use only on the Guildmaster's recorded answer, as for rollouts ([approval in a Steward session](../steward.md#approval-in-a-steward-session)).

The first kind is the **takeover** of a running Linux Director ([takeover](../steward.md#takeover)): pause, drain, back up, publish a first `managed.toml` with no managed key, enrol, reload, resume, and commit the roster entry.
For it the allow-list gains six commands, each with the copy directory last and any other value restricted as the copy directory is:

- `director status --json --copy <copy_dir>`, which only reads;
- `director pause --copy <copy_dir>` and `director resume --copy <copy_dir>`;
- `backup now --copy <copy_dir>`, and `store export --out <dir> --copy <copy_dir>` for a copy with no `[backup]` table (the grill's Q2 (a));
- `director enrol --steward <label> --copy <copy_dir>`, the label being a lowercase id.

`CheckCommand` learns arguments that take values, so `--out` takes an absolute path and `--steward` an id; anything else is refused before `ssh` runs.
The apply holds `.rollout.lock`, writes a step log beside the plan so a later apply resumes at the first step not done, and never acts again for a step done.
On a failure the Steward reads the step log, tells the Guildmaster the diagnosis and may apply the same plan again; any other fix is a new dry run, with a new digest, approved again (the grill's Q1 (a)).

The Steward still writes no copy's `config.toml` in a takeover, and never edits one: the copy's local settings are kept, and the roster is made to agree with the copy, not the other way round.

The second kind is **provisioning** a new Director on a roster machine that already has Guildhall ([provisioning](../steward.md#provisioning)).
It is derived from the machine's own `setup doctor --json`: each fixable gap is a step the Steward closes with `setup doctor --fix --only <id>`, and each human gap a wizard step the Guildmaster closes on the machine, whose completion record the Steward reads back while the doctor decides.
A step file the Steward writes for an install no built-in covers is **plan-bound**: the dry-run plan lists it in full with its digest, the apply copies it with `scp`, and the engine runs it only under that digest, so a file changed after approval stops the apply with `ErrPlanChanged`.
This is the one case where the Steward writes a copy's `config.toml`: for a copy it provisions, rendered in the plan and checked to load before approval.
For it the allow-list gains `setup doctor --json` with `--steps`, `--digest`, `--setup-dir` and `--set` values (and `--fix --only <ids>`), `store init`, `release adopt`, `director start --tab`, and `git clone -- <remote> <path>`, whose remote is an `https://` or `git@` URL in the restricted character set; the longer commands get their own longer timeout.
Step file contents never travel on a command line, only their path and digest.
The Steward never runs a wizard, `sudo` or a human step, and never holds a secret.

A **bare machine**, one with no file at the roster's `guildhall_path`, first gets a provisioning plan that only places Guildhall ([bootstrapping a bare machine](../steward.md#bootstrapping-a-bare-machine), the grill's Q4 (a)): the Steward's own release binary and its launcher are copied there with `scp`, each checked with `sha256sum` before the binary first runs, and the binary installs itself with `release install <version> --from <file> --sha256 <hex>`.
The doctor needs Guildhall on the machine, so the rest of the provisioning is a second dry run of the same request, approved on its own.
For it the allow-list gains `sha256sum <path>`, an absolute path, the one command whose program is `sha256sum`, and `release install <version> --from <file> --sha256 <hex>`, with a release version, an absolute path and a lowercase hex sha256.

## Alternatives rejected

- A general remote shell for the agent to diagnose and fix with: it would undo the [fixed remote commands](steward-fixed-remote-commands.md) record's reason, since quoting differs between servers and roster values are not fully trusted.
- Adding takeover steps to the rollout plan: every rollout digest and approval would change, and a rollout never adds to the roster.
- Committing the roster entry before the steps run: a failed takeover would leave the roster naming a copy that is not managed, which the next rollout would refuse; the entry is committed last.
- Refusing a copy with no `[backup]` table: a local store export is a full, restorable copy that does not depend on d97.
- Bootstrapping a bare machine and provisioning its copy in one plan: the doctor's gaps, which make the plan's steps, cannot be known before Guildhall is on the machine, so the Guildmaster would approve steps not yet derived.

## Consequences

Rollout plans and digests are unchanged.
A machine whose `guildhall` predates a takeover command fails that step with its diagnostic, and the plan resumes there once it is upgraded.
A takeover that is abandoned before the copy is changed resumes the session it paused; once `managed.toml` is published it can only be finished.
Provisioning runs every install as an approved step file on the [setup engine](setup-engine-from-firstmate.md) under the same approval and plan kind; a later slice of d128 adds native Windows.
