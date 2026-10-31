---
name: guildhall-steward-rollout
description: Use as the Steward when the Guildmaster asks to roll out shared settings, check the roster or see what a copy last received, to dry-run, get approval of the exact plan digest and apply it.
---

# Steward rollout and roster

The roster and the shared managed settings live in the private config checkout your config names in `[private_config] location`: `roster.toml` and `managed.toml`.
A rollout copies the managed settings to the Director copies on the machines you name, over SSH, then runs the fixed `guildhall config reload` on each copy it wrote; it never writes a copy's `config.toml` or creates a copy.
Keep every plan under your copy's `rollouts/` directory, one new directory per dry run, so `guildhall steward status` finds its receipts.

## The roster

Run `guildhall steward status` to show the Guildmaster every machine, its SSH alias and its Director copies, with each copy's last rollout receipt.
The roster is edited in the private checkout only on the Guildmaster's word; a roster entry never holds a credential, since the Steward user's SSH config behind each `ssh_alias` holds the address, user and key.

## Steps

1. Dry-run on the machines the Guildmaster named, into a new plan directory:

   ```sh
   guildhall steward rollout --config <your copy>/config.toml --machine <label> --plan <your copy>/rollouts/<name> --dry-run
   ```

2. Show the Guildmaster the plan: each copy's status, every key added, changed or removed, the diff and the plan digest.
   A refused copy is never overwritten: tell the Guildmaster why it was refused, and stop.
3. If the plan needs approval, the dry run says so.
   Open its approval decision and give the Guildmaster the command it prints, which they run themselves, answering `approve` or `decline`:

   ```sh
   guildhall steward approval request --plan <your copy>/rollouts/<name>
   ```

   Never answer it yourself: inside your session `guildhall decide answer` refuses it, and `--approve` is refused until the Guildmaster's answer is recorded.
4. Apply with the same machines and plan, adding `--approve <plan digest>` only once the Guildmaster answered `approve`:

   ```sh
   guildhall steward rollout --config <your copy>/config.toml --machine <label> --plan <your copy>/rollouts/<name> --apply
   ```

   Apply runs `guildhall config reload` over SSH on each copy it wrote, which is how that Director accepts the new settings.
5. Tell the Guildmaster each copy's result from the receipt.
   A copy that failed is reported, never retried behind their back; a plan that is no longer current needs a new dry run.
   A copy that is `reload_failed` has the new file, but its Director refuses it until it is reloaded: give the Guildmaster the error from the receipt, such as an older `guildhall` that does not know `config reload`, and tell them they can run `guildhall config reload --copy <dir>` on that machine once it is fixed.

## Rules

- Never apply a plan the Guildmaster has not seen.
- Never remove `.rollout.lock` while another apply may be running; tell the Guildmaster instead.
- You never message a Director: a rollout is the only way a change reaches a copy.
