---
status: accepted
date: 2026-11-09
topic: director
tasks: d222
amends: own-scripts.md
---

# The task list's own script may request its same-origin JSON feed, list-only scripts are inlined on the list alone, and pinned third-party files may be vendored

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

### Vendored files

Slice 4 of the same spec asked for two 14-day charts in the list's stats strip, drawn by uPlot 1.6.32 (MIT), which [own scripts](own-scripts.md) ruled out with "no third-party code".
This record amends that rule:

- **Named, pinned third-party files are allowed under `internal/web/vendor/`.** Each vendored package is one directory, `internal/web/vendor/<package>/`, holding the upstream files unmodified, the package's licence file and a `manifest.json` naming the package, its version, its SPDX licence, where the files came from (the npm tarball, with its shasum and integrity) and each file's sha256. The licence must be MIT, BSD (2- or 3-clause) or Apache-2.0. Every file of the directory is listed in its manifest, and `loadVendor` refuses to start the server on a file that is missing, unlisted or does not match its pin, so the bytes served are the upstream ones.
- **A vendored script is pinned like an own one.** It is inlined as an attribute-free `<script>` on the one page that uses it, and its hash joins the own scripts' in the one CSP's `script-src`; there is still no `src=` script, no `.js` route and no CDN. A vendored stylesheet is inlined in a `<style>` on that page. The own-script code rules, which forbid `innerHTML` and the like, apply to Guildhall's own files under `scripts/`; a vendored file is trusted for its pin and licence instead, and makes no request (the CSP's `connect-src 'self'` stands either way).
- **Notices.** `THIRD_PARTY_NOTICES.md` names each vendored package, its version, directory, source and licence text; `TestVendorManifests` checks the manifest, the pins and the notice together.
- **uPlot is the one package.** `uPlot.iife.min.js` and `uPlot.min.css` from `uplot-1.6.32.tgz` are inlined on `/` only, with `charts.js`, a list-only own script, drawing the charts from the data the page and the feed carry. Updating uPlot is a new tarball, new pins and a new notice, in one change.

## Consequences

`TestOwnScriptCodeRules` pins each script's one request, so a new request, or one to another path, fails it until a record allows it.
`checkOwnScripts` checks each page against its own set, and `TestScriptSets` that `list.js` is on `/` alone and `poll.js` off it.
The relative-time words changed once, "2 h ago" to "2 hr ago", in `relTime`, the docs and the goldens.
The task list carries about 51 kB more for uPlot; the goldens hold a placeholder in place of the vendored bytes, which the manifest test pins instead.
A vendored file that does not match its pin stops the binary at start, not at the first request, so it cannot ship unnoticed.

## Alternatives rejected

- Vendor timeago.js for relative times: the same display for a third-party file to pin, hash and update.
- A per-page CSP listing only that page's script hashes: more moving parts for no gain, since every listed script is Guildhall's own and does nothing on a page it was not written for.
- Draw the charts by hand as inline SVG or canvas in an own script: hover, axes and resizing over two charts would be several hundred lines to maintain for what uPlot gives pinned and tested.
- Load uPlot from a CDN or a `.js` route: an external request, or a route serving script, which the CSP and the own-scripts record keep out.
- Keep "2 h ago" on the server and write the browser's words by hand: the browser's own formatter says "2 hr ago", and one set of words both sides use is simpler than two.
