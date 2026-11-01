# Task pages

Each task has a web page: what needs you, the task in your words, where it stands and its story, with every document one tap away.
The pages are served privately on your tailnet while a Director session runs, or while you run `guildhall web`; there is no always-on service ([R1.29](requirements.md)).
They work on a phone as well as a desktop, follow your system's light or dark setting, and run no script.

You can answer a task's open decisions on its page, or send the Director your words, from your own Tailscale login ([Answering](#answering)).
The page records what you give and tells the Director; it never advances, sends back or starts a task.

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
   `guildmaster` is your Tailscale login, as `tailscale whois` names it; only that login can answer on the page, and with no `guildmaster` every page is read-only.
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

- **The band** names the repository, task, state and related tasks, the title, and a stepper with one entry per step of the task's workflow: ✓ finished, ↩ sent back, ▸ running, ? waiting on you, and later steps hollow. A decision's mark comes from its answer's tone in the option table: a positive answer is ✓, a negative one ✗, and a carry-on or neutral answer, such as a merge hold or follow-ups none, shows ✓ with its word; ↩ marks an answer the table does not know for its kind, or an attempt sent back. A multi-PR task shows its current slice's steps and "slice k of n".
- **The rail**, on the left, keeps Status (state, step and round, latest attempt, pull request, CI, slices and related tasks, and when the page was read), the Needs you index, the task in your words with what the grill decided, and the documents grouped by kind, newest first.
- **Now** holds every open ask in full: its kind, id and when it opened, the question, for a plan approval the plan's summary from the spec (goal, size, slices, what changed since the grill and the open questions with their recommendations) or a link to the spec when it has none, what it is about with a link to that document, the open findings for a review loop exit, and one tile per option saying what it does, coloured green when it ends the ask well, red when it stops the work and blue when it carries on. Deferred asks follow, marked deferred. A grill the Director runs with you in its session is shown as such, not as an ask.
- **The story**, newest first, shows each attempt, decision, slice merge and send-back, with findings, your answers and the Director's feedback quoted; the six newest show and the rest fold away.

Only your open decisions are asks, as in Clerk's `/guild`: an answered decision the Director has not yet continued, a void decision and Guildhall's own install notice are not.
The pull request and CI are the latest the store holds; the page makes no forge call.
Nothing updates by itself: the rail says when the page was read, so reload for newer.
When the Director is writing, a page may say the store is busy; reload in a moment.

On a phone the band carries chips for the step, the pull request and CI, the stepper becomes a thin progress bar, and the asks come first, then the story, then the task, the documents and Status.

## Answering

The Director's reply links each decision it lists under Needs you, such as `- d122 · approve the plan · accept-1 · http://andrew-lap-wsl.tail5da40a.ts.net:4388/guildhall/d122#accept-1`, so a tap opens the ask.

Each ask has one box, **Your words**, and below it the answer tiles:

- **An answer.** Tap a tile. Your words, if any, are recorded with it as your note, exactly as `decide answer` records `<option>`, a blank line and your words, and the decision records that you answered on the page and your login.
  The Director gets a `decision-answered` message naming the decision, not your words, reads the answer with `decide show` and continues the task, without asking you again.
  The page reloads at the ask, which has left Now and shows on its story node.
  An ask with no options, a free question, has an **Answer with your words** tile instead.
- **Send words only**, under the dashed line, answers nothing: your words reach the Director as a note from you, naming the task and decision, shown to you in a toast as `messages send` shows a note, and the page says they were sent.

Below the tiles the page says "Answering as <your login> · your Tailscale login".
Any other login, or a request whose login Guildhall cannot tell, sees the same page read-only, with the tiles disabled and "Only the Guildmaster's Tailscale login can answer here".

An answer is refused, with nothing written, the reason on the ask and your words kept in the box, when:

- it does not come from your login: each request is checked with `tailscale whois` on its address, so a missing or failing `tailscale` leaves the page read-only;
- it does not come from the page itself: its `Origin` header, or its `Sec-Fetch-Site` when the browser sends no `Origin`, must be the page's own, and it must carry the page's form token, which changes when the server restarts, so reload a page left open across one;
- the decision changed since you loaded the page: it was voided, or it is now about another version of its subject; or it was already answered, in chat, with `decide answer` or on the page;
- the task was cancelled: a cancel needs its asks voided or answered first, so only an ask an earlier release left open can still show on a cancelled task;
- your answer, with your words, is over 64 KiB, words only are blank or over 16 KiB, or the text is not UTF-8;
- the Director is writing to the store: try again in a moment.

Your words are normalised as `decide answer` reads an answer file: a byte order mark is dropped and Windows line endings become plain ones.
Tapping twice records the first answer once.

## The reader page

Every document link opens `http://<address>/<repo>/<task>/doc/<name>`: the document, rendered from Markdown with goldmark at its safe defaults ([decision](decisions/goldmark-for-documents.md)), with the open asks, answerable as on the task page, and Status beside it and a way back to the task.
**Raw** gives the file as plain text.
An HTML document is shown only in a sandboxed frame, and any other file as plain text.
A document is shown only when its file still matches the sha256 recorded for it; a missing or changed file is refused with a page that says which.

## Limits

- No grills on the page, no front page of everything waiting and no browsing of all tasks: these come later. A grill round is still answered in the Director's session.
- HTTP only, inside the tailnet; no HTTPS.
- The Steward serves no pages.
