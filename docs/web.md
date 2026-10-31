# Task pages

Each task has a web page: what needs you, the task in your words, where it stands and its story, with every document one tap away.
The pages are served privately on your tailnet while a Director session runs, or while you run `guildhall web`; there is no always-on service ([R1.29](requirements.md)).
They work on a phone as well as a desktop, follow your system's light or dark setting, and run no script.

The pages only read.
They never advance, send back or start a task, and in this release you answer a decision in chat or with `guildhall decide answer`, as before; answering on the page comes next.

## Set up

1. Install the `guildhall` that knows `[web]` first.
   Config decoding refuses unknown keys, so an older binary would refuse the whole config, and with it every command.
2. Add the table to the copy's `config.toml` ([the web table](configuration.md#the-web-table)):

   ```toml
   [web]
   address = "andrew-lap-wsl.tail5da40a.ts.net:4388"
   guildmaster = "andrew@example.com"
   ```

   Use this machine's Tailscale name and a port of this copy's own: each Director copy on a machine needs its own port.
   `guildmaster` is your Tailscale login; answering on the page will use it.
3. Start or reload the Director session.
   `director start` names the address in its `web` line, and `director status --json` gives a `web` object: `serving` with the address, `failed to bind` with the reason, `not serving` when no session serves it, or `not configured`.

The server binds only when every address the host resolves to is a Tailscale address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`).
Anything else, such as a LAN address or `0.0.0.0`, is refused and logged in the session's pane as a warning; the session runs on without the pages.
Anyone on your tailnet can read the pages.
A request for any other host name, including the bound IP or the short MagicDNS name, is refused with 421, so open the pages by the `[web]` address, which is what `director` prints.

With no session running, serve the pages by hand from inside the copy, or with `--copy`, until you press Ctrl-C:

```sh
guildhall web [--copy <dir>]
```

## The task page

Open `http://<address>/<repo>/<task>`, such as `http://andrew-lap-wsl.tail5da40a.ts.net:4388/guildhall/d122`; each ask has its own anchor, `#<decision-id>`.

- **The band** names the repository, task, state and related tasks, the title, and a stepper with one entry per step of the task's workflow: ✓ finished, ↩ sent back, ▸ running, ? waiting on you, and later steps hollow. A multi-PR task shows its current slice's steps and "slice k of n".
- **The rail**, on the left, keeps Status (state, step and round, latest attempt, pull request, CI, slices and related tasks, and when the page was read), the Needs you index, the task in your words with what the grill decided, and the documents grouped by kind, newest first.
- **Now** holds every open ask in full: its kind, id and when it opened, the question, what it is about with a link to that document, the open findings for a review loop exit, and one tile per option saying what it does, coloured green when it ends the ask well, red when it stops the work and blue when it carries on. Deferred asks follow, marked deferred. A grill the Director runs with you in its session is shown as such, not as an ask.
- **The story**, newest first, shows each attempt, decision, slice merge and send-back, with findings, your answers and the Director's feedback quoted; the six newest show and the rest fold away.

Only your open decisions are asks, as in Clerk's `/guild`: an answered decision the Director has not yet continued, a void decision and Guildhall's own install notice are not.
The pull request and CI are the latest the store holds; the page makes no forge call.
Nothing updates by itself: the rail says when the page was read, so reload for newer.
When the Director is writing, a page may say the store is busy; reload in a moment.

On a phone the band carries chips for the step, the pull request and CI, the stepper becomes a thin progress bar, and the asks come first, then the story, then the task, the documents and Status.

## The reader page

Every document link opens `http://<address>/<repo>/<task>/doc/<name>`: the document, rendered from Markdown with goldmark at its safe defaults ([decision](decisions/goldmark-for-documents.md)), with the open asks and Status beside it and a way back to the task.
**Raw** gives the file as plain text.
An HTML document is shown only in a sandboxed frame, and any other file as plain text.
A document is shown only when its file still matches the sha256 recorded for it; a missing or changed file is refused with a page that says which.

## Limits

- No answering on the page yet, no grills on the page, no front page of everything waiting and no browsing of all tasks: these come later.
- HTTP only, inside the tailnet; no HTTPS.
- The Steward serves no pages.
