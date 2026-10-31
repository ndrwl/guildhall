---
status: accepted
date: 2026-10-22
topic: director
formerly: D57, 0057-director-conversation-first.md
---

# Plain English to the Director is the Guildmaster's primary interface, and the commands stay complete and public

## Context

The README led with the terminal, because no conversational Director was built yet, though the Guildmaster's main way to use Guildhall was meant to be talking to their Director.

## Decision

The Guildmaster talks to their Director in plain English, and the Director turns each request into `guildhall` commands through its rendered skills; the commands are the Director's tools, not the Guildmaster's daily interface.
The README leads with this conversation.

The commands stay complete and public: a Guildmaster may still call them to script, to inspect a task or when no Director runs, and an outside monitor such as the status band (R1.26) reads status through them without becoming state or lifecycle authority.
The store remains the only record: a conversation is never state, so a request that changes anything must end in a command and its receipt.

## Alternatives rejected

- Leading with the terminal: it made the Director's tools the Guildmaster's daily interface.
- Commands only the Director may call: scripting, inspection and a Guildhall with no Director running still need them.

## Consequences

The [Director's skills](director-skills.md) must cover everything a Guildmaster would otherwise type: giving work, asking what is waiting on them, inspecting tasks, approving or stopping work, and answering decisions in conversation.
For a decision the Director maps the Guildmaster's reply onto an option, such as `approve`, keeps their words as the note, records it with `decide answer` and continues the task, since answering never advances a task by itself.
A new Guildmaster-facing command is not complete until a Director skill can call it, and a capability the Director cannot reach through a command is a gap to close, not a reason to send the Guildmaster to the terminal.
