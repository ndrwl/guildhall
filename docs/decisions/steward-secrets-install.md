---
status: accepted
date: 2026-11-13
topic: steward
tasks: d242, d227
amends: steward-secrets-push.md, steward-fixed-remote-commands.md
---

# A secrets push drops the ciphertext beside the machine's file, and an allow-listed secrets install swaps it in only once it decrypts there

## Context

A [secrets push](steward-secrets-push.md) copied the ciphertext with `scp` straight over the machine's `~/.config/guildhall/secrets.sops.yaml` and only then read it back and ran `secrets check`.
A cut-short put, a read-back mismatch or a file the machine's key could not decrypt left bad bytes live with no previous copy, and every Director start on that machine then refused (d227's hallmark finding H104).
Recovery needed a new commit, plan and approval.

## Decision

- Apply puts the ciphertext to `secrets.sops.yaml.new-<sha256>` beside the live file, on the same filesystem, and reads that file back.
- The allow-list gains one entry: `secrets install --expect <sha256> --copy <copy_dir>`. It refuses a key or file inside the copy, a dropped file that is missing or has another digest, and one that does not decrypt with the machine's key, by the same check `secrets check` runs. It then copies the live file, if any, to `secrets.sops.yaml.prev` and renames the dropped file over the live name in one rename, so the live name always holds the old or the new ciphertext. It removes any other dropped file, prints a JSON report of names and digests and exits 1 on a refusal.
- A push's commands are put, read-back, `secrets install` and `secrets check`; a refused install is the receipt status `install_failed`, and `applied` keeps its meaning.
- `secrets check --json` reports `installs: true`, so the plan refuses a machine whose older `guildhall` cannot install.
- There is one `.prev`, overwritten by each install, and no remote restore command; recovery is a manual `mv` ([secrets](../secrets.md#secrets-install)).

## Alternatives rejected

- Keeping a revision history of old ciphertext: retirement rotation would have to consider every kept file.
- A remote restore or delete command: another allow-list entry and approval path for a manual `mv` and a harmless leftover.
- A human form of `secrets install` beside `--json`: install is for the Steward, and one shape keeps one allow-list entry.

## Consequences

Every secrets plan written before this change goes stale, since its commands are in its digest; apply refuses it with "plan stale", and a new plan is needed.
A cut-short put leaves a partial `.new-<sha256>` file until the next successful install removes it; `secrets.Load` reads only the live name, so it is harmless.
