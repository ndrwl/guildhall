---
name: guildhall-steward-startup
description: Use as the Steward when a Steward session starts and its first message names startup.md, to read your notes, work every in-flight rollout approval, then run guildhall steward ready.
---

# Steward startup

A new Steward session starts `recovering`: it starts no new work until you say it is ready.
The first message names the numbered session's `startup.md`; use that exact path.
In Claude Code the first message is `/version startup "<path>"`: reply first with only the version output, as that command says, then read `startup.md` at that path.
It holds your live notes under `## Notes`, if any, and the in-flight items: each rollout approval waiting for the Guildmaster and each one they approved.

## Steps

1. Read `startup.md` at the exact path in the first message.
2. Read the `## Notes` section, if there is one, and keep it current with `guildhall director note edit` and `guildhall director note remove`, which work on your own copy.
3. Run `guildhall steward status` for the current session, roster, approvals and each copy's last rollout.
4. Work each in-flight item in order:
   - a rollout approval that waits for the Guildmaster: tell them which plan it is and what it changes, and wait for their answer;
   - a rollout plan the Guildmaster approved: apply it with `guildhall-steward-rollout` unless `guildhall steward status` shows its receipt.
5. Read your inbox with `guildhall messages list steward`, handle each message in order and acknowledge it with `guildhall messages ack steward <id>`.
6. When nothing in flight is yours to act on now, run `guildhall steward ready`.

```sh
guildhall steward status
guildhall steward ready
```

`guildhall steward ready` prints the session as one JSON line with state `running`.
Then tell the Guildmaster in a few plain sentences what you recovered, what is still waiting on them and what happens next.

## Rules

- Never start a rollout while the session is `recovering`.
- To hand off to a fresh session, as when your context fills, run `guildhall steward handoff`; a reload the Guildmaster asks for is `guildhall steward reload`.
- You never message a Director, and nothing you do needs one to be running.
