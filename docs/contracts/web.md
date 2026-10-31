# `internal/web`

## Contract

`internal/web` serves the Guildmaster's task pages ([user doc](../web.md), [decision](../decisions/task-page.md)): one read-only HTML page per task and a reader page per recorded document, laid out as d105's approved design-3.
It renders and routes only, on `net/http`, `html/template` and `embed`; its one module is goldmark ([decision](../decisions/goldmark-for-documents.md)).
It never opens the store and never imports `internal/cli`.

### The seam

`Source` is the one interface: `Task(repo, id) (Task, error)` returns one task's page model, `ErrNotFound` for a task the copy does not hold and `ErrBusy` when a writer holds the store past the source's bounded wait.
Each call opens the store, reads and closes it before returning, so no page renders with the store open.
`Task` is plain values: title, state, Clerk's where-it-stands phrase, related tasks, the stepper's `Step`s, a multi-PR task's `Slices`, `Status` (step and round, latest attempt, latest pull request and its checks), the open and then deferred `Ask`s, the Director stage it waits on, its other needs_me reasons, the Guildmaster's words, its `Document`s (its own and other tasks' linked ones, each with path, sha256, group, step, round and recorded time) and its story `Node`s, oldest first, each in one `StepState`.
`internal/cli`'s `webSource` fills it from Clerk's readers (`clerkReadWait`, `stands`, the status feed's decisions, needs_me and slices, `timeline` and `files`), so Clerk's own output is unchanged; the asks are the task's share of Clerk's Needs you split: its open decisions only, never an answered decision waiting for `task start`, a void decision or the copy's install item, and a `director-stage` wait is shown as the Director running that stage, not as an ask.
`internal/web` is tested with a fake `Source`; `webSource` against a real temporary store.

### Routes

`Server` routes `GET` and `HEAD` only:

- `/<repo>/<task>`: the task page, each ask anchored `#<decision-id>`;
- `/<repo>/<task>/doc/<name>`: the reader page of one of the task's documents;
- `/<repo>/<task>/doc/<name>/raw`: the document as `text/plain; charset=utf-8`;
- `/<repo>/<task>/doc/<name>/frame`: an HTML document only, as `text/html` with `Content-Security-Policy: sandbox` (`FrameCSP`), shown in an `<iframe sandbox>` on its reader page.

Each segment is path-escaped; an empty, `.` or `..` segment, a trailing slash, any other path and any other method is 404.
A document resolves only by a name the task's `Task` records for that task; its file is read from the record's slash path under `Options.Documents`, never from the URL, refused if the path climbs out, and served only when its sha256 matches the record.
A missing file is 404, a mismatch 409, and an unknown name 404, each a refusal page that names the document and never a filesystem path or an internal error.
A Markdown document (`.md`) is rendered by `RenderMarkdown`; an HTML one is never rendered inline; any other is shown as escaped text.
`ErrBusy` is 503 "The store is busy"; any other source error is a 500 with no detail, its error passed to `Options.Log`.

Every response carries `CSP` (`default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; frame-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'`) but the frame route's, with `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
A page is executed into a buffer first, so a template failure is a plain 500, not half a page.

### Pages

One embedded template (`page.tmpl`) and one stylesheet (`style.css`), inlined; no script, no external request, `prefers-color-scheme` themes, the phone layout reflowed from the same markup at 760 px, and 44 px tap targets.
Only `RenderMarkdown`'s output and the embedded stylesheet reach the template unescaped; pull request URLs are linked only when they are `http` or `https`.
In this slice every ask is read-only: disabled answer tiles, no form, no text box, and the line "Answer in chat or with `guildhall decide answer` for now; answering on this page comes in the next slice".
The story shows, for an open task, one hollow "Later" node naming the workflow's steps not yet started, then its six newest nodes, then folds the rest under "N earlier entries".
The Needs you index names each open ask's kind, the document its subject matches or else its short subject digest or else its options, and its `#id`.
The rail's "What the grill decided" fold is the newest grill notes document's `## Decisions` section rendered by goldmark, counted by its top-level list items, or a link to the document when it has none or its file does not verify.

### Rendering, options and binding

- `RenderMarkdown` is goldmark with its tables extension at its safe defaults: raw HTML omitted and `javascript:`, `vbscript:`, `file:` and `data:` links dropped.
- `OptionMeaning(kind, option)` reads the option-meanings table: a one-line meaning and a `Tone` (positive, negative, carry on) for every option of every set `interp.DecisionOptions`, `render.DecisionOptions`, `store.DecisionOptions` and `cli.StewardDecisionOptions` export; an option it does not describe shows its word alone, neutral.
- `BindAddress(ctx, address, lookup)` resolves a `[web]` address and returns its first address with the port, refusing an address with no host or port, a host that does not resolve, and with `ErrNotTailnet` a host any of whose addresses lies outside `100.64.0.0/10` and `fd7a:115c:a1e0::/48`; `Listen` binds what it returns. `URL(address, repo, task)` is a task page's link.
- `Serve(ln, h)` serves with bounded header, read, write and idle times; `Running.Close` lets requests finish for up to five seconds, then closes them, and returns once stopped.

### Tests

- `page_test.go`: the design-3 fixture (two open asks, three grill rounds, a sent-back review with a fixed finding, a deferred merge, a void superseded acceptance, a two-slice task with slice 1 merged, a Director-run grill) renders every section, anchor, state class, "slice 2 of 2", the void node, a viewport, no `<script>`, no `<form>` and only the pull request links, with the CSP header; it is checked in as `testdata/task.golden.html` (`-update` rewrites it) to open beside design-3; hostile titles, questions, options, words, findings, document names and URLs are escaped; an empty task renders each empty line; the routes' refusals; documents served on a match, refused without a path on a mismatch, a missing file, an unknown name and a climbing record path, Raw as `text/plain`, the frame sandboxed, another task's document only on its own route.
- `markdown_test.go`: tables, nested lists, fenced code and quotes render; raw HTML and the unsafe link schemes are omitted; `FuzzMarkdownIsSafe` asserts no `<script` tag or `on…=` attribute reaches the output; `BindAddress` accepts Tailscale addresses only, IPv4-mapped included, and `Listen` refuses loopback.
- `options_test.go` (package `web_test`): every exported option has a meaning and a tone; an unknown option or kind is neutral.
- `internal/cli/web_test.go`: `webSource`'s asks follow Clerk's split before and after an answer and for a void decision, a Director stage, a multi-PR task's slices and merge, `ErrNotFound` and `ErrBusy`; `guildhall web` serves until stopped and refuses no `[web]` and a loopback address; a session's serving record and director status's `web` object; a supervised Director session serves, binds again after a reload and stops at its end, a bind failure only warns, and a Steward session's watchers serve nothing.
