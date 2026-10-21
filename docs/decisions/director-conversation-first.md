# Plain English to the Director is the primary interface

The Guildmaster's main way to use Guildhall is talking to their Director in plain English.
The Director turns each request into `guildhall` commands through its rendered skills; the commands are the Director's tools, not the Guildmaster's daily interface.
The README leads with this conversation and marks it planned until the Director slice lands; it previously led with the terminal because no conversational Director is built yet.

The commands stay complete and public.
A Guildmaster may still call them directly to script, to inspect a task or when no Director runs, and an outside monitor such as the status band (R1.26) reads status through them without becoming state or lifecycle authority.
The store remains the only record: a conversation is never state, so a request that changes anything must end in a command and its receipt.

## Consequence

The Director's skills must cover everything a Guildmaster would otherwise type.
That includes giving work, asking what is waiting on them, inspecting tasks, approving or stopping work, and answering decisions in conversation: the Director maps the Guildmaster's reply onto the decision's option, such as `approve`, keeps their words as the note, records that answer with `decide answer` and continues the task, since answering never advances a task by itself.
A new Guildmaster-facing command is not complete until a Director skill can call it, and a capability the Director cannot reach through a command is a gap to close, not a reason to send the Guildmaster to the terminal.
