# Task pages

A task list shows every task, grouped by who it waits on, and each task has a web page: what needs you, the task in your words, where it stands and its story, with every document one tap away.
The pages are served privately on your tailnet while a Director session runs, or while you run `guildhall web`; there is no always-on service ([R1.29](requirements.md)).
They work on a phone as well as a desktop, follow your system's light or dark setting, and run only Guildhall's own scripts: small ones built into Guildhall and pinned by hash, never anything a document, Artisan or pull request supplies. Every page still works with scripts turned off.

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
   `director start` names the address in its `web` line, and `director status --json` gives a `web` object: `serving` with the address, `failed to bind` with the reason, `not serving` when no session serves it, or `not configured`, and its `links`, how the Director links a decision to its page ([status](director.md#status)).

The server binds only when every address the host resolves to is a Tailscale address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`).
Anything else, such as a LAN address or `0.0.0.0`, is refused and logged in the session's pane as a warning; the session runs on without the pages.
Anyone on your tailnet can read the pages.
A request for any other host name, including the bound IP or the short MagicDNS name, is refused with 421, so open the pages by the `[web]` address, which is what `director` prints.

With no session running, serve the pages by hand from inside the copy, or with `--copy`, until you press Ctrl-C:

```sh
guildhall web [--copy <dir>]
```

## The task list

Open `http://<address>/`, such as `http://andrew-lap-wsl.tail5da40a.ts.net:4388/`, for every task of the copy, in every repository it serves, grouped by who it waits on:

- **Waiting for you**, marked amber: a task with an open or deferred ask, or a grill the Director runs with you in its session.
- **Moving**: a task whose step is starting or running, waits for a free slot, or waits for CI or the merge queue; it needs nobody.
- **Waiting on something else**: a task waiting on its dependencies or the Director, paused, or not started.
- **Finished in the last 7 days**, folded last: the tasks done or cancelled this week, as Clerk's `/guild all` lists them.

The band counts them, such as "3 tasks wait for you · 5 are moving · 3 wait on something else".
Each row names the task, linked to its page, its title, its where-it-stands sentence, the same one its page shows, and since when: the oldest open ask's opening for a task waiting for you, the closing for a finished one, and otherwise the last change recorded on the task.
Open tasks are listed in Clerk's order, by priority then id; finished ones newest first.
The list has no forms: answer on each task's page.
Every task page's crumb starts with **All tasks**, a link back to the list.

## The task page

Open `http://<address>/<repo>/<task>`, such as `http://andrew-lap-wsl.tail5da40a.ts.net:4388/guildhall/d122`; each ask has its own anchor, `#<decision-id>`.

- **The band** names the list, the repository, task, state and related tasks, the title, one sentence on where the task stands, such as "**Waiting for you on 2 asks:** whether to go on after the slice 2 review, and approval of the plan for slice 2.", with the slice, pull request and CI after it in grey, and a stepper with one entry per step of the task's workflow: ✓ finished, ↩ sent back, ▸ running, ● waiting on you, and later steps hollow. A decision's mark comes from its answer's tone in the option table: a positive answer is ✓, a negative one ✗, and a carry-on or neutral answer, such as a merge hold or follow-ups none, shows ✓ with its word; ↩ marks an answer the table does not know for its kind, or an attempt sent back. A multi-PR task shows its current slice's steps.
  Under the sentence, a task whose workflow has a grill shows its [grill mode](tasks.md#grill-mode) as `task show` prints it, such as "grill: unattended (director): contracts already written" or "grill: live". An unattended grill's stepper entry is named "Unattended grill (Artisan)" and says finished, not a count of rounds.
- **The rail**, on the left, keeps Status (where it stands, step and round, the last attempt, pull request, CI, slices and related tasks, and when the page was read), the Needs you index, the task in your words with what the grill decided, and the documents grouped by kind, newest first.
- **Now** holds every open ask in full: its kind, id and when it opened, a readable headline such as "The plan for slice 2 is ready for your approval.", [the Director's explanation](#the-directors-explanation), for a plan approval the plan's summary from the spec (goal, size, slices, what changed since the grill and the open questions with their recommendations) or a link to the spec when it has none, what it is about with a link to that document, the open findings for a review loop exit, and one tile per option saying what it does, coloured green when it ends the ask well, red when it stops the work and blue when it carries on. Deferred asks follow, marked deferred. A grill the Director runs with you in its session is shown as such, not as an ask; its open round follows the asks as one form ([grill rounds](#grill-rounds)).
- **The story**, newest first, shows each attempt, decision, slice merge and send-back as a sentence, such as "You approved the plan" or "The review sent the build back (round 1)", with the attempt's own summary, findings, your answers and the Director's feedback quoted, and a folded "Raw events" list of the records it was made from; the six newest show and the rest fold away.

The page names records in words: the stepper says "plan approved" for `accept-2 approved`, Status says "Slice 2 review, attempt 2" for `review--s2-2`, and Documents says "Grill notes, round 3" for `grill-3-notes`.
Each keeps its record id as its title, shown when you hover over it, and in the story's Raw events, so nothing is lost.
These words are fixed templates the page fills in; they cost no tokens, and Clerk's own words are unchanged.
An ask's headline is its story sentence, and the Needs you index and the where-it-stands line name it by the same short phrase, such as "approval of the plan for slice 2"; the question's own first line, such as "Approval needed for guildhall/d122, step accept--s2 …", leads "The rest of the question", folded.

### The Director's explanation

Under each ask's headline, above its summary and questions, the Director's own explanation, what the ask is about, what each answer means and what it recommends, shows as "From the Director · <time>".
The Director writes it once, with [`decide explain`](decisions-cli.md#decide-explain), when the ask opens, so its chat message can be one Needs you line and a link.
It is Markdown rendered as documents are: raw HTML is dropped and unsafe links are blanked, while ordinary links are kept.
When the explanation names a recommended answer, it becomes the ask's one [recommendation](#confirm-and-say), and that answer's tile is marked **Recommended** and comes first, the same mark a plan approval's Approved carries; an ask that marks its own recommended answer keeps its mark, and the Director's is ignored.
An ask the Director has not explained says so in grey, pointing at the rest of the question for the full request; an explanation written for an earlier version of the ask, whose subject has since changed, is not shown, and the note says why.

Only your open decisions are asks, as in Clerk's `/guild`: an answered decision the Director has not yet continued, a void decision and Guildhall's own install notice are not.
The pull request and CI are the latest the store holds; the page makes no forge call.
Nothing updates by itself: the rail says when the page was read, so reload for newer; the [new-reply bar](#the-new-reply-bar) tells you when the Director has written.
When the Director is writing, a page may say the store is busy; reload in a moment.

On a phone the band carries the where-it-stands sentence and chips for the pull request and CI, the stepper becomes a thin progress bar, and the asks come first, then the story, then the task, the documents and Status.

## Answering

The Director's reply links each decision it lists under Needs you with a Markdown link, such as `- d122 · approve the plan · accept-1 · [open](http://andrew-lap-wsl.tail5da40a.ts.net:4388/guildhall/d122#accept-1)`, so a click opens the ask even when the link wraps across lines.

Each ask has one box, **Your words**, and below it the answer tiles:

- **An answer.** Tap a tile. Your words, if any, are recorded with it as your note, exactly as `decide answer` records `<option>`, a blank line and your words, and the decision records that you answered on the page and your login.
  The Director gets a `decision-answered` message naming the decision, not your words, reads the answer with `decide show` and continues the task, without asking you again.
  The page reloads at the ask, which has left Now and shows on its story node.
  An ask with no options, a free question, has an **Answer with your words** tile instead.
- **Send words only**, under the dashed line, answers nothing: your words reach the Director as a note from you, naming the task and decision, shown to you in a toast as `messages send` shows a note, and the page says they were sent.

On a plan or design approval, each open question with lettered options is a pick: tap an option on its card to change that answer, with the recommended option picked to begin with.
On a plan approval whose open questions have [controls](#plan-open-questions), a changed pick is that question's answer, as described there; what follows is any other approval's.
A changed pick shows "Your answer: Q2 → (b)" on its card, and the answer row hides Approved and puts **Changes requested** forward; this needs a browser with CSS `:has()`, and the page has no script.
What is recorded:

- **Changes requested** records your changed picks as one "Q2 (b)" line each, in question order, then a blank line and your words, or the lines alone when you typed nothing, so the plan or grill goes round again with them as feedback, as typing "Q2 (b)" did. Unchanged picks write nothing.
- **Approved** with every pick unchanged records exactly what it did before; with a changed pick it is refused, nothing written, with "you changed Q2; choose Changes requested".
- **Send words only** puts the pick lines before your words in the same way.
- Any other answer ignores the picks.

Typing "Q2 (b)" in the words box, or in chat, still works.

Below the tiles the page says "Answering as <your login> · your Tailscale login".
Any other login, or a request whose login Guildhall cannot tell, sees the same page read-only, with the tiles disabled and "Only the Guildmaster's Tailscale login can answer here".

An answer is refused, with nothing written, the reason on the ask and your words and picks kept, when:

- it does not come from your login: each request is checked with `tailscale whois` on its address, so a missing or failing `tailscale` leaves the page read-only;
- it does not come from the page itself: its `Origin` header, or its `Sec-Fetch-Site` when the browser sends no `Origin`, must be the page's own, and it must carry the page's form token, which changes when the server restarts, so reload a page left open across one;
- the decision changed since you loaded the page: it was voided, or it is now about another version of its subject; or it was already answered, in chat, with `decide answer` or on the page;
- the task was cancelled: a cancel needs its asks voided or answered first, so only an ask an earlier release left open can still show on a cancelled task;
- you pressed Approved after changing a pick, or a pick field is malformed;
- your answer, with your words and pick lines, is over 64 KiB, words only are blank or over 16 KiB, or the text is not UTF-8;
- the Director is writing to the store: try again in a moment.

Your words are normalised as `decide answer` reads an answer file: a byte order mark is dropped and Windows line endings become plain ones.
Tapping twice records the first answer once.

## The conversation

Each task has one conversation: the talk between you and the Director about it, oldest first and append-only, kept in the store ([task conversation](store.md#task-conversation)).
Each message is tagged with the attempt or decision it is about, such as `grill-2` or `accept-1`, and is Markdown, rendered as safely as the documents: goldmark at its safe defaults, so no raw HTML and no script.

Messages get there wherever the words already pass through Guildhall, each written in the same store transaction as the record it goes with:

- **Send words only** on an ask records your words, as you wrote them, under that ask, from the page;
- an answer given in chat, which the Director records with `decide answer`, records your answer under its decision, from chat;
- a question the Director opens with `decide open` records the question under it, from the Director;
- `guildhall task say --repo <id> --task <id> --tag <attempt|decision> --text-file <path|->` writes the Director's own words, such as its reply to your note, under the attempt or decision; with `--author guildmaster-chat` it quotes words you said about the task in chat that no command records. A tag must name an attempt or decision of the task, and running the same command again writes nothing new;
- **Send to the Director** in the Say box records your words under the current attempt, from the page ([Confirm and Say](#confirm-and-say));
- **Confirm** records that you confirmed the Director's recommendation, with the answer it gave.

A tile answer on the page records no message: your answer and its words show on its story node.

On the page each message is a full-width row: a head line with who wrote it (**You**, on your own login's page, or **Guildmaster**, with `page` or `chat`, or **Director**) and when, over its body, with a bar on its left, blue for the Director and grey for you.
Where it shows follows its tag:

- **Under its ask.** An open ask's talk sits inside its card, under "Talk on #<decision>", after the question and its summary and before **Your words** and the tiles, at the anchor `#<decision>-talk`.
- **On Now.** The current stage's talk, on its latest attempt, sits under the asks as "Talk on <stage · attempt> · oldest first", its newest three messages shown and the earlier ones folded as "*n* earlier messages". Talk whose tag has neither an ask nor a story node also shows on Now, so nothing is left off the page.
- **In the story.** Every other attempt's or decision's talk folds under its story node as "Talk · *n* messages", closed. A node whose talk is on Now says so.

Each ask's header has a grey chip naming its stage and id, such as `review · review--s2-x2`, which links to its talk, or to its story node when it has none; each story node has the anchor `#story-<tag>`.
The rail's Status gains a **Talk** line, "*n* messages · last *hh:mm*".

A done or cancelled task's conversation stays readable and takes nothing new; Now says "This task is done and merged. Its talk stays below, read-only", or that it is cancelled, and its talk folds into its story.

## Confirm and Say

When you send words only on an ask, the Director replies under it on the page.
When your words read as an answer, its reply is a **recommendation**: its reading of your words and the answer it recommends, written with `guildhall task say --tag <decision> --recommend <option> --subject-digest <digest> --text-file -`, one of the ask's options, against the digest of what it read.
Its row's head says "recommends <answer>", with an amber bar while the ask is open.

Under the ask's talk an amber box, **Recommended by the Director**, holds one tile, **Confirm: <answer>**, and the **Your words** box below it is filled with your words on the ask, or with the words the Director gave with `--words-file`, for you to edit.
So an ask with a recommendation reads, in order: the question, its summary and open questions, its talk with Confirm, Your words, then the answer tiles.

- **Confirm** answers the ask with the recommended option and the words in the box, through the same answer path as a tile, so tapping twice answers once.
  The talk records that you confirmed it, naming the recommendation, as "↩ Confirmed: <answer>" over your words, and the recommendation's bar is no longer amber.
- The ordinary tiles stay, so you can still answer differently.
- Nothing is answered until you tap. A newer recommendation replaces the Confirm on the page, and confirming one the Director has since replaced is refused: reload and read the new one.

Each ask has one recommendation, the Director's latest, whether it came from `task say --recommend`, from its explanation (`decide explain --recommend`) or from a question it opened with `guildhall decide open --recommend <option>` ([store](store.md#decision-recommendations)).
That one recommendation marks its option's tile **Recommended**, so the badge and Confirm always name the same answer; only a recommendation from `task say` has a Confirm, so when a later explanation replaces one, the badge moves and Confirm goes.
A recommendation made against an earlier version of the ask than the one it is now about is stale: it marks no tile and has no Confirm, and the ask says "The Director recommended <answer> against an earlier version; it is stale."; confirming it is refused with "the Director's recommendation on #<id> is stale; reload and read it", and nothing is written.

At the end of the current stage's talk, on an open task, your page has a **Say to the Director about <attempt>** box, even when nothing is asked.
**Send to the Director** answers nothing: it records your words under that attempt and sends them to the Director as a note, naming the task and attempt and shown to you in a toast as words only are, and the Director replies in the same talk.
It is refused as an answer is, with your words kept in the box, and also on a task that has closed since the page loaded.

Every form on the page posts through one gate: the page must answer at all, the request must be from your login, from this page and carry its form token, each checked as [answering](#answering) describes.

## Grill rounds

A grill the Director runs with you puts each round on the page, under its attempt, with `guildhall grill round open --repo <id> --task <id> --attempt <attempt> --questions-file <path|->`.
The round is the questions in the [`guildhall-grill`](../skills/guildhall-grill/SKILL.md) format, each a `**Q<n>. <title>**` line, its body and a last line `Recommended: <answer>`, numbered from Q1; the command refuses any other text, an attempt that is not a running Director stage, a closed task, and a new round while the attempt's last one is not sent, and running it again writes nothing new.
A resumed or reopened grill is a new attempt, so its rounds open under that attempt.
In chat the Director only lists the round in Needs you, with a link to `#<attempt>`, such as `#grill-2`.

On Now, after the asks, the open round is one card, **Grill round <k>**, anchored at its attempt, and one form, posted to `POST /<repo>/<task>/round`:

- each question shows its title, body and the Director's recommendation, a **Your words** box and two buttons, **Accept recommendation** and **Answer with my words**;
- an answered question folds to one line, "✓ Accepted" or "✓ Your words, saved", with its gist and **Change**, which opens it again with its words;
- a box holding words that are not an answer yet says "saved *hh:mm* · not an answer yet";
- the bar at the bottom of the card, which sticks to the bottom of the window while the card is in view, has **Save, send later** and **Send round**, whose tile says "*n* still to answer".

Every button saves every box first, as drafts: they are not answers, never go through the answer path and stay until the round is sent ([drafts](store.md#drafts)).
So nothing typed is lost whichever button you press, or if you leave and come back.
**Send round** can always be pressed: while a question has no answer it saves every box and then refuses, naming them, such as "Q9 and Q10 have no answer yet; your words are saved".
Once every question is answered it records the round's answers on the conversation and a `grill-answered` message to the Director in one store transaction, and wakes it; pressing it twice sends once.
The message names the round and the command that prints the answers, `guildhall grill round show --repo <id> --task <id> --attempt <attempt> [--round <k>]`, never your words.
The sent round folds to one Director row, such as "Round 1 · 4 questions · sent 21:06 · 3 accepted, 1 your words", which opens to each question and its answer.

The page never advances the task: the Director reads the answers, opens the next round or finishes the grill with `step finish` as before.
The rail's Needs you counts the open round as an ask, with its progress, such as "Grill round 2 · 4 of 6".
A round that was never sent shows as "⊘ … not sent"; once its task closes its saved drafts are dropped, and it says so.
A round's buttons are refused as an answer is, with your words kept in the boxes, and also when the round was sent meanwhile, the Director opened a newer one or its attempt has finished, or its answers were saved from another tab or window since you loaded the page.
Then nothing is saved: reload, and the words saved from the other tab are there; pressing the same button again after a dropped connection is not refused.
A tab left open across an upgrade of Guildhall is refused in the same way once its round has any saved draft, and must be reloaded.

## Plan open questions

A plan approval asks its spec's open questions inside the approval, one at a time, with a grill round's controls.
Under the Summary, "Open questions · *n* of *m* answered" heads the spec's question cards, and each card has a **Your words** box, **Accept recommendation**, naming the recommended option, and **Answer with my words**; an answered question folds to one line with **Change**, as a round's does, and **Save, answer later** saves every box.
The controls are buttons of the approval's one form, posted to `/answer`, and each of them, and the approval's **Approved** and **Changes requested** tiles, saves every box first as drafts under the decision ([drafts](store.md#drafts)); **Send words only** answers nothing and saves no box.
There is no Send round: the approval's answer sends them.

A card that is not yet answered also keeps its [picks](#answering): picking another option is a third way to answer that question.
Each question has one answer, from whichever you touched last: a changed pick becomes the question's answer, saved as a draft marked picked when you press any button, its card then folding to "✓ Picked (b) …" with **Change**, unless the button you press is that question's own **Accept recommendation** or **Answer with my words**, which wins over the pick; a folded card shows no picks, and **Change** reopens it with the recommendation picked.
While a pick is changed, Approved stays, and its line and that of **Changes requested** read "with your picks and answers above; the rest take the recommendation".
A pick is written once, as that question's line in the list below, never as a separate "Q2 (b)" line; **Send words only**, which answers nothing, still puts the pick lines before your words.

**Approved** or **Changes requested** writes the answers into the one answer, after the option and your words, as a Markdown list under "Open questions, answered on the page:", one line per question:

```text
- Q1: accepted the recommendation (b) one route
- Q2: 15 s, not 5 s.
- Q3: picked (c) a table
- Q4: no answer; takes the recommendation (a) list
```

Words in the box of an accepted or picked question follow its answer after a semicolon.

A question with no answer takes the spec's recommendation, and the tiles say which, such as "with your 2 answers; Q3 and Q4 take the recommendation".
Words saved in a box but not marked are not an answer.
With no question answered the answer is the approval alone, which takes every recommendation, as before.
An answer that would go over the 64 KiB limit with its list is refused, and your drafts are kept.
Only the Guildmaster's login's page shows the controls; a read-only page shows the cards alone.

## The new-reply bar

While your page of an open task is open, it checks every 5 seconds whether the Director has written on the task, and every minute while the tab is in the background.
When the Director has, a blue bar at the top of Now, on a desktop and on a phone, says "New from the Director on #<decision> · <time> · **Reload**", and the tab's title starts with the number of new messages, such as "(2)".
**Reload** opens the page again at that talk, `#<decision>-talk`.
The page never reloads by itself, so nothing you are typing is lost.

When Guildhall has restarted since the page loaded, the bar says the page is out of date and its forms would be refused: reload before you answer.

The check asks the page's own server only, reads only the task's talk, never the whole task, and writes nothing; only your login's page has the bar.
With scripts turned off there is no bar, and the rail's "reload for newer" still applies.
The [own-scripts decision](decisions/own-scripts.md) records why this one script may make a request.

## The reader page

Every document link opens `http://<address>/<repo>/<task>/doc/<name>`: the document, rendered from Markdown with goldmark at its safe defaults ([decision](decisions/goldmark-for-documents.md)), with the open asks, answerable as on the task page, and Status beside it and a way back to the task.
**Raw** gives the file as plain text.
An HTML document is shown only in a sandboxed frame that runs no script and loads nothing from the network, and any other file as plain text.
A design ask shows the design's mock-up: a web-page mock-up is an **Open mock-up** link beside the link that reads the design, which opens it in that frame in a new tab at the full window width, and sample CLI or chat output is shown in the ask as preformatted text.
A document is shown only when its file still matches the sha256 recorded for it; a missing or changed file is refused with a page that says which.

## Limits

- No push notifications: the [new-reply bar](#the-new-reply-bar) shows only on a page that is open.
- HTTP only, inside the tailnet; no HTTPS.
- The Steward serves no pages.
