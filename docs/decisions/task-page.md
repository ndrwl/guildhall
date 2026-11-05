---
status: accepted
date: 2026-10-31
topic: director
tasks: d101, d105, d122, d145
amended-by: own-scripts.md
---

# The Guildmaster's task pages are served by the Director's session supervisor, on the tailnet only, from Clerk's readers

## Context

The Guildmaster asked for every decision to be "simply a web link that I can click at any time", on one page per task with its full context and status, usable on a phone (d101, d105, d122).
R1.29 allows no always-on Guildhall service, and the copy's store has a single writer.
The Guildmaster approved d105's design-3 and plan, and asked for two slices: read-only pages first, then answering.

## Decision

`internal/web` serves one page per task and a reader page per document ([user doc](../web.md), [contract](../contracts/web.md)).
It runs only while a Director session runs, from the Director role's `watch` hook, binding again at every session and closing before the session's end is recorded, or from a hand-run `guildhall web`; a Steward session serves nothing, and a bind failure only warns.
The optional `[web]` table names the address per copy ([configuration](../configuration.md#the-web-table)); the server binds only when every address its host resolves to is in Tailscale's ranges, so it fails closed, and anyone on the tailnet can read.
`internal/web` never opens the store: a `Source` filled by `internal/cli` from Clerk's readers opens it per request with a bounded wait, reads, closes, and only then is the page rendered, so the page and Clerk agree and Clerk's output is unchanged.
Its asks are the task's share of Clerk's Needs you split.
Answer tiles say what each option does from one option-meanings table in `internal/web`, which a test keeps covering every option set the engine, the questions, grants and the Steward export.
Pages carry a CSP that allows no external request, and only Guildhall's own scripts, pinned by hash ([own scripts](own-scripts.md), which replaces this record's first rule of no script at all); of those, one script, `poll.js`, may ask the page's own server whether the Director has written (d145), and nothing else may make a request; HTML documents are shown only in a sandboxed frame.
In the first slice every ask was read-only.

In the second slice the Guildmaster answers on the page ([Answering](../web.md#answering)):

- **Identity is `tailscale whois` per request.** The `[web]` table's `guildmaster` names their Tailscale login, and each request's remote address is looked up with `tailscale whois`; only that login gets the forms, and only its POSTs are taken. Any other login, a missing or failing `tailscale`, or no `guildmaster` leaves every page read-only. Tailnet-only binding already keeps everyone off the tailnet out; whois tells the Guildmaster from others on it with no password or session of Guildhall's own.
- **The configured address, same origin and a form token.** Every request must name the `[web]` address as its `Host`, or it is refused before anything is read, so a DNS-rebinding page that points its own name at the tailnet address cannot read a page or its token under the Guildmaster's login. A POST must carry the configured address's `Origin`, compared with the `[web]` address and never with the request's own `Host`, or, when a browser sends none or `null`, `Sec-Fetch-Site: same-origin`, and a per-process random token from the page's form. Pages are served with `Referrer-Policy: same-origin`, since under `no-referrer` browsers send a form's POST with `Origin: null`.
- **One answer path.** The answer goes through `answerDecision`, the same function as `decide answer`, under the same mutation id, so the cancelled-task refusal, the store's checks and the double-submit replay are unchanged, and an answer is recorded once whichever way it comes. Before it, the page refuses a decision voided or rebound to another subject since the page loaded, and a Confirm whose recommendation was made against an earlier subject than the decision's ([one recommendation per decision](../store.md#decision-recommendations), H62 of d181).
- **Who answered.** `store.Decision` gains `answered_via` (`page`) and `answered_by` (the login), both optional, set only for a page answer and part of its request digest; `decide answer` leaves them empty, so its receipts and old records are unchanged. `decide show`, `clerk inspect` and the page's story show them.
- **The Director is told, not handed the words.** A page answer sends a registered `decision-answered` message from the Guildmaster naming the decision only; the Director reads the answer with `decide show` and continues with `task start --decision`, as for a chat answer. Words only are a `note` from the Guildmaster prefixed with the task and decision, surfaced in a toast as `messages send` surfaces a note, so the inbox skill's note row stays true. Both wake the Director with sender type `page`, whose line carries counts only.

The Director's reply links each decision's Needs you line to its ask when `director status` shows the pages' address.

d145 makes the page where the Guildmaster and the Director talk about a task ([The conversation](../web.md#the-conversation)), which adds routes but no new way in:

- **Several POST routes, one gate.** `POST /<repo>/<task>/answer` is joined by `/say`, the Say box, and `/round`, a grill round's buttons. Every POST passes one gate, factored out of the answer route: the server answers at all, the whois login is the Guildmaster's, the request is same-origin by `Origin` or `Sec-Fetch-Site`, and the form token matches; every route keeps the Host check. Answers still go only through `answerDecision`; the Say box and a round answer nothing.
- **One script that asks.** `poll.js` asks `GET /<repo>/<task>/since` every 5 s while the tab is visible and every 60 s while it is hidden, for the [new-reply bar](../web.md#the-new-reply-bar). The route is behind the Host check and for the Guildmaster's login only, refuses another site's `Sec-Fetch-Site`, writes nothing, and reads only the conversation's tail from a read-only store open, never Clerk's read, so it holds the store's shared lock only for that read. The CSP gains `connect-src 'self'` and nothing else; `FrameCSP` is unchanged, so documents still run no script. The page never reloads itself, and with scripts off it works as before.

## Alternatives rejected

- An always-on service: R1.29 forbids it.
- Binding on all interfaces with an access check: one mistake exposes the copy to the LAN; tailnet-only binding fails closed.
- Moving Clerk's readers into a shared package: it risks Clerk's byte-for-byte output, which the seam keeps.
- A password or Guildhall login for answering: one more secret to keep, where the tailnet already knows who each node belongs to.
- Trusting any tailnet request to answer: anyone the Guildmaster shares the tailnet with could approve a merge.
- Putting the Guildmaster's words in the `decision-answered` message: the store already holds them on the decision, and the message would duplicate them.

## Consequences

No session, no pages, unless `guildhall web` is run by hand.
The option-meanings table must change with every new option the engine offers, which the coverage test enforces.
`[web]` is strictly decoded, so it is added to a copy only after the binary that knows it is installed.
Answering needs `tailscale` on the serving machine's PATH; without it the pages are read-only.
A page left open across a server restart carries a stale token, and its answer is refused until it is reloaded; with scripts on, the new-reply bar says so before the Guildmaster submits, from the server's start id, which the since route returns and which is not the token.
