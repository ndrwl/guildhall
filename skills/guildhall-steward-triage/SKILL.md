---
name: guildhall-steward-triage
description: Use as the Steward to collect Directors' lessons and incidents into the private config and present each triage decision to the Guildmaster, when they ask, a triage-opened message arrives or startup lists an open triage decision.
---

# Steward collection and triage

Directors file lessons and maintenance incidents in their own copies with `guildhall lesson propose` and `guildhall incident file`.
Collection brings those of the enrolled copies into the private config checkout, and triage is the Guildmaster's answer to each.

## Collect

Run collection when the Guildmaster asks, or after they answer triage decisions, so their answers are written back:

```sh
guildhall steward collect [--machine <label>]
```

It reads each enrolled copy's pending items over SSH, commits them as `lessons/<machine>/<origin>-<id>.md` and `incidents/<machine>/<origin>-<id>.json`, one commit per machine, pushes the private config, marks the items collected on each copy and opens one triage decision per new item.
It prints the round as JSON: each copy's items, any copy it could not reach or mark, the decisions it opened and the answers it recorded.
Tell the Guildmaster what it collected and what it could not; an unreachable machine is collected in a later round.
`busy` means a rollout or another collection holds `.rollout.lock`: wait and run it again, and never remove the lock yourself.
With `[steward] collect_every` set, your session also collects on that schedule, and a round that opens decisions sends you a `triage-opened` message.

## Present each decision

The startup snapshot and the `triage-opened` message name the open decisions, `steward/incidents/<id>` and `steward/lessons/<id>`.
For each, read it with `guildhall decide show --repo steward --task <incidents|lessons> --decision <id>`, read the item's file in the private config, and tell the Guildmaster in plain English what the Director reported, from which machine and copy, with the evidence.
Give them the command they answer it with themselves, outside your session:

```sh
guildhall decide answer --copy <your copy> --repo steward --task <incidents|lessons> --decision <id> --answer-file -
```

An incident's options are `accept`, `defer` and `decline`; a lesson's are `adopt` and `decline`.
Never answer a triage decision yourself: inside your session `guildhall decide answer` refuses it.

## After the answer

Run `guildhall steward collect` again to write each answer into its item's file and push it.

- An accepted incident is for the Guildmaster to create a task; say so, and do nothing to any Director.
- An adopted lesson is yours to draft as an ordinary change to the private config, such as a managed setting, which ships only through a later rollout the Guildmaster approves.
- A deferred incident stays in the private config for a later look; a declined item is closed.

## Rules

- Triage authorises no work: you never start, stop or message a Director about an item.
- Never edit a collected file by hand; its triage is written by `guildhall steward collect`.
