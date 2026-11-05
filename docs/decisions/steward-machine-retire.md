---
status: accepted
date: 2026-11-04
topic: steward
tasks: d130
amends: steward-secrets-push.md
---

# Retiring a machine rotates every value it could read, tracked in a record open until each is rotated

## Context

A machine's secrets are sops+age ciphertext encrypted to its own key and the recovery key ([decision](secrets-sops-age.md)), pushed by the Steward ([decision](steward-secrets-push.md)).
A lost machine keeps its key and the ciphertext it was given, and the private config's history keeps every file, so removing its recipient cannot take back what it could read.
d130 slice 4 has the Steward give the Guildmaster an approved plan that revokes the machine and rotates every value it could read, and keeps the plan open until every value is rotated.

## Decision

- `steward machine retire <machine>` plans, changing nothing, then applies once the Guildmaster approves the plan's digest, on the reserved task `steward/secrets` as a secrets push is. A machine already retired, open or closed, is refused.
- Apply makes one commit to the private config: it removes the machine's `age_recipient` line from `roster.toml`, regenerates `.sops.yaml`, removes `secrets/<machine>.sops.yaml` and writes `retired/<machine>.json`. Each machine has its own file, so no other file is re-encrypted.
- The plan lists every name in the machine's file as a manual rotation step and the machine's own GitHub, SSH, Herdr, Claude and Codex credentials as revocation steps. Guildhall cannot revoke at a provider, and sign-ins were never copied, so these are printed for the Guildmaster.
- The record is the receipt: it holds a rotation for every name, and stays open until each closes, with its date.
  - A name another machine holds is one rotation per such machine, closed when `secrets set` gives it a new value there, in the same commit. A value the retired machine could read does not close it: the one the machine already held, or the one in the retired machine's own file, decrypted with the recovery key at the commit the plan read it at.
  - A name no other machine holds has nothing to set, so it is one rotation by name alone, closed when the Guildmaster runs `steward machine retire --rotated <NAME> <machine>` after rotating or revoking it at its provider. The command is refused inside a Guildhall session, so only the Guildmaster can say a value was rotated.
- The machine stays in the roster, and nothing runs on it: a lost machine cannot be reached.

## Alternatives rejected

- Re-encrypting old files without the machine's key: history and the machine's own copy keep the old ciphertext, so it revokes nothing.
- Removing the whole roster entry: it would also drop its Directors from rollouts and status, a separate decision from revoking its secrets.
- A Guildmaster mark for every rotation: `secrets set` already marks the moment a new value exists where a machine holds the name; the mark is needed only where none does.
- Treating a name only the retired machine held as revoke-only, with nothing to wait for: a machine whose values are all its own would close at once with nothing rotated, against the spec's plan that stays open until every value is rotated.
- Approving each mark through a decision, as for a plan: the mark carries no plan to bind a digest to, and the Guildmaster, who did the rotation, can run it directly.

## Consequences

The Guildmaster removes a retired machine's roster entry by hand once its Directors are gone.
Guildhall cannot check that a marked value was really rotated at its provider; the mark records the Guildmaster's word and its date.
