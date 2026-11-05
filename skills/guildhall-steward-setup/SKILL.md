---
name: guildhall-steward-setup
description: Use as the Steward when the Guildmaster asks to take over an existing Director copy or provision a new one, to dry-run the plan, get approval of its exact digest, apply it, resume it after a failure or wizard step, or abandon a takeover.
---

# Steward setup: taking over a Director

A takeover adopts a running standalone Director copy into the roster as managed by you, keeping its store, tasks and local settings.
It is one setup plan, approved by its exact digest: pause the session, wait for running attempts, back up the store, publish a first `managed.toml` with no managed key, enrol, reload, resume, and commit the roster entry.
Keep every plan under your copy's `rollouts/` directory, one new directory per takeover, such as `rollouts/takeover-<director>`.

## Steps

1. Ask the Guildmaster for the machine's roster label, the Director's name and its copy directory on that machine, and, for a machine not in the roster yet, the SSH alias that reaches it.
2. Dry-run the takeover:

   ```sh
   guildhall steward takeover --config <your copy>/config.toml --machine <label> --director <name> --copy <copy dir> --plan <your copy>/rollouts/takeover-<name> --dry-run
   ```

   Add `--ssh-alias <alias>` only for a machine not in the roster.
   A refusal changes nothing: tell the Guildmaster why in plain English, such as a repository whose `owner_machine` names another machine, a copy that already has a `managed.toml`, or a roster that already lists the Director, and stop.
3. Show the Guildmaster the plan: the copy and its session, the roster diff, every step and the plan digest.
   Say that the session will be paused while running attempts finish, and that the copy's settings do not change.
4. Open the approval decision and give the Guildmaster the answer command it prints, which they run themselves:

   ```sh
   guildhall steward approval request --plan <your copy>/rollouts/takeover-<name>
   ```

   Never answer it yourself: inside your session `guildhall decide answer` refuses it, and `--approve` is refused until the Guildmaster's answer is recorded.
5. Once the Guildmaster answered `approve`, apply:

   ```sh
   guildhall steward takeover --config <your copy>/config.toml --plan <your copy>/rollouts/takeover-<name> --apply --approve <plan digest>
   ```

6. Read the last line of the output and tell the Guildmaster what happened:
   - `done`: the copy is managed by you and in the roster; confirm with `guildhall steward status`.
   - `waiting drain: …`: attempts are still running and the session is paused; apply the same command again later, and tell the Guildmaster how many attempts are still running.
   - `failed <step>: …`: give the Guildmaster the diagnosis and the fix it names, such as an unreachable machine, an older `guildhall` that does not know a command, or a partial export to remove; apply the same plan again once they say it is fixed.
     A plan refused as changed needs a new dry run and a new approval.

## Abandoning

If the Guildmaster no longer wants the takeover before its `managed` step ran, abandon it, which resumes a session the takeover paused:

```sh
guildhall steward takeover --config <your copy>/config.toml --plan <your copy>/rollouts/takeover-<name> --abandon
```

Once the `managed` step has run, the takeover can only be finished with `--apply`; say so.

# Steward setup: provisioning a new Director

Provisioning makes a new managed Director copy on a roster machine that already has Guildhall: it fixes the machine's fixable gaps, waits for the Guildmaster at each human one, writes the copy's `config.toml` and `managed.toml`, makes its store, clones its repositories, adopts the release, enrols, reloads, starts its Herdr tab, commits the roster entry and writes a verified summary.
Keep each plan in a new directory such as `rollouts/provision-<director>`.

1. Ask the Guildmaster for the roster machine, the Director's name, its copy and setup directories there, the Herdr session its tab opens in, each repository's id, remote and path, and any pins; write them as a request file in your copy (`docs/steward.md#provisioning`).
   When the machine has no Guildhall yet, add `bootstrap = true` and make sure the roster names its `guildhall_path`, such as `/home/<user>/.local/bin/guildhall`, in a directory that exists; with Go and Herdr missing too, ask for their pins (`docs/steward.md#bootstrapping-a-bare-machine`).
   When the machine needs an install no built-in step file covers, write a step file of your own beside the request and name it in `step_files`; every change it makes must be a `check`, `action` and `verify`, and anything that needs administrator rights a `human` step.
2. Dry-run:

   ```sh
   guildhall steward provision --config <your copy>/config.toml --request <request file> --plan <your copy>/rollouts/provision-<name> --dry-run
   ```

   A refusal changes nothing: tell the Guildmaster why, such as a copy directory that already has a config or store, a clone path that is not empty, or a missing Herdr with no pins, and stop.
3. Show the Guildmaster the plan: the doctor's gaps, every step, each step file you wrote in full with its digest, the config it writes and the plan digest.
   Say which steps are theirs to run on the machine, including any Guildhall fact the doctor found missing: the pinned Guildhall release (`guildhall-pin`), `guildhall_path` on the `PATH` of SSH commands (`guildhall-on-path`), Tailscale SSH without a check-mode rule (`tailscale-ssh`) and a running Herdr server for the session (`herdr-session`).
4. Open the approval decision as for a takeover, and give them its answer command; never answer it yourself.
5. Once they answered `approve`, apply:

   ```sh
   guildhall steward provision --config <your copy>/config.toml --plan <your copy>/rollouts/provision-<name> --apply --approve <plan digest>
   ```

6. Read the last line:
   - `done` after `guildhall-install`, a bootstrap plan: Guildhall is on the machine; make the dry run again with the same request in a new plan directory, and go on from step 3 with that plan.
   - `done`: give the Guildmaster the summary line: the tab, the versions against the pins and any outstanding manual item; confirm with `guildhall steward status`.
   - `waiting <step>: …`: a human step; give the Guildmaster the one `guildhall setup wizard …` command it prints, to run themselves on the machine, and what the record shows, and apply the same command again once they say it is done.
     Never run the wizard yourself.
   - `failed <step>: …`: give the diagnosis and the doctor's action line, and apply the same plan again once it is fixed.
     A fix outside the plan, such as a new step file of yours, is a new dry run and a new approval.

## Rules

- Never apply a plan the Guildmaster has not seen and approved.
- Every change to the machine is a step of the approved plan; a fix outside it is a new dry run, approved again, and never a command you run on the machine yourself.
- Never remove `.rollout.lock` while another apply or a collection may be running; tell the Guildmaster instead.
- You never message the Director you take over.
