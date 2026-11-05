---
status: accepted
date: 2026-11-04
topic: director
tasks: d175, d145
amends: task-page.md
---

# Pages may run Guildhall's own scripts, embedded in the binary and pinned by hash in the CSP

## Context

[The task pages](task-page.md) carried no script, enforced by tests and by a CSP with `default-src 'none'` and no `script-src`.
In the grill of d175 (s40) the Guildmaster asked to loosen the rule ("surely we should allow some scripts"), so a page can, for example, fill the Your words box as an answer is picked (d174).
The real risk is not script itself but untrusted text gaining script: anything Artisans, pull requests or documents supply.
The Guildmaster delegated the grill's questions; these are the Director's recommendations, accepted on that delegation.

## Decision

Pages may run only **own scripts**: small, hand-written files under `internal/web/scripts/`, embedded in the binary ([contract](../contracts/web.md#own-scripts)).

- **Pinned by hash, not nonce.** Each file is read once when the package loads and kept as `template.JS` with its `'sha256-<base64>'` hash computed from the same bytes, so the inlined text and its hash cannot drift. `CSP` becomes a `var` built once: today's policy plus `script-src` and the hashes, and nothing else, no `'unsafe-inline'`, `'unsafe-eval'`, `'unsafe-hashes'`, `'strict-dynamic'`, `'self'`, nonce or host. With no own scripts it is today's string. Hashes keep the one policy on every response and need no per-request state.
- **Inlined, static.** The task list, task and reader pages inline each own script in an attribute-free `<script>`; the refusal page and the mock-up frame carry none. No template action goes inside a script; pages pass data only through DOM elements and `data-*` attributes. There is no `.js` route and no `src=` script.
- **Untrusted content never runs script.** `RenderMarkdown`, escaping, `FrameCSP` and the `<iframe sandbox>` stay as they are, and the browser refuses any script whose hash is not listed, so a script that slipped into content still could not run.
- **Code rules.** No HTML parsing from strings, no string evaluation, no network calls (but `poll.js`'s one, below) or module loads, listeners only through `addEventListener`, and no submitting; a test searches the embedded files for the forbidden calls.
- **Progressive enhancement.** Every page and every answer works with scripts off, and the server keeps every check it has: Host, token, Origin or `Sec-Fetch-Site`, login and request shape.
- **No third-party code.** No libraries, CDNs or vendored frameworks, and no external request.
- **A first script ships now.** `js.js` adds the class `js` to `<html>`, so real pages, the goldens and a browser exercise the hash pinning before d174 builds on it; no style uses the class yet.

### One request, for the new-reply bar (d145)

d145's accepted plan asked for the [new-reply bar](../web.md#the-new-reply-bar) as "one embedded same-origin script, `poll.js`", with its own GET route and `script-src 'self'`; this record had meanwhile pinned own scripts by hash with no `.js` route and no network call.
`poll.js` follows this record's form instead: it is an own script under `internal/web/scripts/`, inlined and pinned by hash like the others, so `script-src` stays hashes only, which is stricter than `'self'`.
The one exception is a request: the CSP gains `connect-src 'self'`, and `poll.js` alone may call `fetch`, once, a same-origin `GET` of the since route its page's bar names in `data-since`, written exactly as `TestOwnScriptCodeRules` pins it. Every other own script still makes no request, and none sets or reloads `location`.
The route writes nothing, answers only the Guildmaster's login and refuses another site's `Sec-Fetch-Site`; the page never reloads itself.

## Consequences

Tests that checked a whole page for `<script` now strip exactly the own scripts first, so hostile content is still proven inert, and a test proves the helper reports any other script.
A new own script is one file under `internal/web/scripts/`; its hash enters the CSP with no other change.
