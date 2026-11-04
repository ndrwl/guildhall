---
name: guildhall-steward-setup
description: Use as the Steward when the Guildmaster asks to take over an existing Director copy and bring it under your management, to dry-run the takeover, get approval of the exact plan digest, apply it, resume it after a failure or abandon it.
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

## Rules

- Never apply a plan the Guildmaster has not seen and approved.
- Every change to the machine is a step of the approved plan; a fix outside it is a new dry run, approved again, and never a command you run on the machine yourself.
- Never remove `.rollout.lock` while another apply or a collection may be running; tell the Guildmaster instead.
- You never message the Director you take over.
