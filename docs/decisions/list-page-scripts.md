---
status: accepted
date: 2026-11-09
topic: director
tasks: d222
amends: own-scripts.md
---

# The task list's own script may request its same-origin JSON feed, and list-only scripts are inlined on the list alone

## Context

d222 rebuilt the all-tasks page (`/`) as a triage list whose rows should change while it sits open, keep counting their relative times up, and sort and filter in the browser without a reload.
[Own scripts](own-scripts.md) allowed one request, `poll.js`'s GET of its task page's since route, and inlined every own script on the task list, task and reader pages alike.
The accepted spec (d222, slice 3) asked for one list-only script, `list.js`, that reads the list's data from `GET /list.json`, under `connect-src 'self'` with `default-src 'none'` kept.
For Q1 of that spec the recommended answer, the browser's own `Intl.RelativeTimeFormat`, was taken over a vendored time library.

## Decision

- **Own scripts may request same-origin JSON.** An own script may `fetch` a read-only JSON route of its own page's server, written exactly as `TestOwnScriptCodeRules` pins it; `list.js` makes one such request, a same-origin `GET` of `/list.json`, and `poll.js` keeps its one. No other own script makes any request, and none sets or reloads `location`. The CSP keeps its one `connect-src 'self'`, added for `poll.js`, and `default-src 'none'`; nothing else is added. The feed carries data only, never HTML, and the script draws it with `createElement` and `textContent` from an inert `<template>` on the page.
- **List-only scripts.** `list.js` is inlined on `/` only and `poll.js` on the task and reader pages only (`listOnly` and `pageOnly` in `internal/web/scripts.go`); every other own script, such as `js.js`, is on all three. The one CSP lists every own script's hash, so each page's set is allowed and no page needs its own policy.
- **Relative times in the browser.** `list.js` rewrites each `<time>`'s text every 30 s and after each refresh with `Intl.RelativeTimeFormat("en-GB", {numeric: "always", style: "short"})`, from its `datetime`, and takes a date past 30 days from its exact-time `title`, so it never changes the title. The server's words are the same ones (`relTime`: "now", "6 min ago", "2 hr ago", "3 days ago", "4 Oct"), so the text agrees with scripts on or off; no time library is vendored.
- **The page still stands alone.** With scripts off, `/` is the server's view of the URL's filter and sort, read as of its load.

Vendored third-party files are a separate rule, for the list's charts, and are not part of this record.

## Consequences

`TestOwnScriptCodeRules` pins each script's one request, so a new request, or one to another path, fails it until a record allows it.
`checkOwnScripts` checks each page against its own set, and `TestScriptSets` that `list.js` is on `/` alone and `poll.js` off it.
The relative-time words changed once, "2 h ago" to "2 hr ago", in `relTime`, the docs and the goldens.

## Alternatives rejected

- Vendor timeago.js for relative times: the same display for a third-party file to pin, hash and update.
- A per-page CSP listing only that page's script hashes: more moving parts for no gain, since every listed script is Guildhall's own and does nothing on a page it was not written for.
- Keep "2 h ago" on the server and write the browser's words by hand: the browser's own formatter says "2 hr ago", and one set of words both sides use is simpler than two.
