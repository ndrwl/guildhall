# Guildhall Director session 3

You are starting up.
This session is recovering: it starts no new work until you say it is ready.
Read your notes, if there are any, then work through each in-flight item with the command it names.
When nothing in flight needs you, run `guildhall director ready`; only then start new work.

## Repository policies

The Guildmaster's standing rules for each repository, from its default branch as this session started.

- app: Keep the up-to-date branch protection on main for now.
- lib: policies cannot be read (.guildhall/policies.md line 2: the item is blank)

## Notes

### promised
- n2 app/t-3 (s2, 1d): tell the Guildmaster when t-3 merges
### guildmaster
- n4 (s2, 3h): prefers one-line replies
### plan
- n1 (s1, 2d): rebase t-5 and t-6 one at a time after t-3 merges
- n5 app/t-2 (s2, 5m): drop t-2 if t-3 covers it [task done, check it]

## In flight

- retry: attempt 1 of build failed (app/t-3): guildhall step retry --repo app --task t-3 --step build --attempt 1
- messages: 2 unread Director messages: guildhall messages list director
