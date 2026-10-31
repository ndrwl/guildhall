# `internal/web`

## Contract

`internal/web` serves the Guildmaster's task pages ([user doc](../web.md), [decision](../decisions/task-page.md)): one HTML page per task and a reader page per recorded document, laid out as d105's approved design-3, on which the Guildmaster's Tailscale login answers the task's open decisions or sends the Director words.
It renders and routes only, on `net/http`, `html/template` and `embed`; its one module is goldmark ([decision](../decisions/goldmark-for-documents.md)).
It never opens the store and never imports `internal/cli`.

### The seam

`Source` is the one interface: `Task(repo, id) (Task, error)` returns one task's page model, `ErrNotFound` for a task the copy does not hold and `ErrBusy` when a writer holds the store past the source's bounded wait.
Each call opens the store, reads and closes it before returning, so no page renders with the store open.
`Task` is plain values: title, state, Clerk's where-it-stands phrase, related tasks, the stepper's `Step`s, a multi-PR task's `Slices`, `Status` (step and round, latest attempt, latest pull request and its checks), the open and then deferred `Ask`s, the Director stage it waits on, its other needs_me reasons, the Guildmaster's words, its `Document`s (its own and other tasks' linked ones, each with path, sha256, group, step, round and recorded time) and its story `Node`s, oldest first, each in one `StepState`.
`internal/cli`'s `webSource` fills it from Clerk's readers (`clerkReadWait`, `stands`, the status feed's decisions, needs_me and slices, `timeline` and `files`), so Clerk's own output is unchanged; the asks are the task's share of Clerk's Needs you split: its open decisions only, never an answered decision waiting for `task start`, a void decision or the copy's install item, and a `director-stage` wait is shown as the Director running that stage, not as an ask.
`internal/web` is tested with a fake `Source`; `webSource` against a real temporary store.

Two more interfaces answer, and with either missing, or `Options.Guildmaster` empty, every page is read-only and every answer refused:

- `Identifier`: `Login(ctx, remote)` returns the Tailscale login of the node at a request's remote `ip:port`, or an error matching `ErrNoIdentity`; an address that does not parse or lies outside the tailnet is refused without asking anyone. `Whois` is the real one: it runs `tailscale whois --json <remote>`, bounded at five seconds, and reads `UserProfile.LoginName`; a missing or failing `tailscale`, bad JSON or no login is `ErrNoIdentity`. `webtest.Logins` is the fake, and `webtest.RunIdentifier` the contract both pass.
- `Answerer`: `Answer(Answer)` records an option, with the words after a blank line, attributed to the page and the login, then messages and wakes the Director; an empty option answers an option-less decision by the words alone. `Send(Answer)` sends the words as a note, answering nothing, and returns its id, `NoteID`. Each refuses with a `*Refusal` (an HTTP status and the reason the ask shows), returns `ErrBusy` for a held store, and writes nothing when it refuses. `internal/cli`'s `webAnswerer` implements it over `answerDecision`, `normaliseText`, the store mailbox, `surface` and `messaging.Wake`.

### Routes

`Server` routes `GET` and `HEAD`, and `POST` to one path:

- `/<repo>/<task>`: the task page, each ask anchored `#<decision-id>`;
- `/<repo>/<task>/doc/<name>`: the reader page of one of the task's documents;
- `/<repo>/<task>/doc/<name>/raw`: the document as `text/plain; charset=utf-8`;
- `/<repo>/<task>/doc/<name>/frame`: an HTML document only, as `text/html` with `Content-Security-Policy: sandbox` (`FrameCSP`), shown in an `<iframe sandbox>` on its reader page;
- `POST /<repo>/<task>/answer`: a form with `decision`, `subject` (the digest the page showed, empty for none), `option`, `send` (`words` for words only, `answer` for an option-less decision's words), `text` and `token`, the server's per-process random form token.

Every route first refuses, with a 421 refusal page and before reading anything, a request whose `Host` is not `Options.Host` (`internal/cli` sets it to the `[web]` address): the host part in any letter case, the port exactly, and a portless or trailing-dot `Host` refused; with no `Options.Host` every request is refused. So a DNS-rebinding page served under another name never sees a page or its token.
Without `Options.Host`, `Options.Identity`, `Options.Guildmaster` and `Options.Answerer` the server does not answer: every page is read-only and every `POST` refused.
An answer is accepted only when, in order: `Identifier` names `Options.Guildmaster`'s login (403); its `Origin` is `http(s)://<Options.Host>`, matched as the `Host` is, never one taken from the request's `Host`, or, with no `Origin` or the opaque `null`, its `Sec-Fetch-Site` is `same-origin` (403); its token matches in constant time (403); it names a decision and an option or a `send` (400); and the `Answerer` takes it.
A refusal writes nothing and renders the task page again with its status, the reason on the ask (class `refused`) and the words in its box; a refusal about a decision no longer among the asks shows at the top of Now.
An answer redirects 303 to `/<repo>/<task>#<decision>`, words only to `/<repo>/<task>?ask=<decision>&sent=<note id>#<decision>`, where the ask shows the words-only-sent state (class `sent`).
The body is bounded at 1 MiB.

Each segment is path-escaped; an empty, `.` or `..` segment, a trailing slash, any other path and any other method is 404.
A document resolves only by a name the task's `Task` records for that task; its file is read from the record's slash path under `Options.Documents`, never from the URL, refused if the path climbs out, and served only when its sha256 matches the record.
A missing file is 404, a mismatch 409, and an unknown name 404, each a refusal page that names the document and never a filesystem path or an internal error.
A Markdown document (`.md`) is rendered by `RenderMarkdown`; an HTML one is never rendered inline; any other is shown as escaped text.
`ErrBusy` is 503 "The store is busy"; any other source error is a 500 with no detail, its error passed to `Options.Log`.

Every response carries `CSP` (`default-src 'none'; style-src 'unsafe-inline'; img-src 'self'; frame-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'self'`) but the frame route's, with `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` and `Cache-Control: no-store`; the policy is `same-origin`, not `no-referrer`, because under `no-referrer` a browser sends a form's `POST` with `Origin: null`.
A page is executed into a buffer first, so a template failure is a plain 500, not half a page.

### Pages

One embedded template (`page.tmpl`) and one stylesheet (`style.css`), inlined; no script, no external request, `prefers-color-scheme` themes, the phone layout reflowed from the same markup at 760 px, and 44 px tap targets.
Only `RenderMarkdown`'s output and the embedded stylesheet reach the template unescaped; pull request URLs are linked only when they are `http` or `https`.
For the Guildmaster's login every ask, deferred ones too, is a form: the Your words box, one submit tile per option, Send words only under a dashed line, and "Answering as <login> · your Tailscale login"; the reader page's asks are the same forms.
For any other login or no identity every ask is read-only: disabled tiles, no form, no box, and "Only the Guildmaster's Tailscale login can answer here", naming the login when there is one.
The story shows, for an open task, one hollow "Later" node naming the workflow's steps not yet started, then its six newest nodes, then folds the rest under "N earlier entries".
The Needs you index names each open ask's kind, the document its subject matches or else its short subject digest or else its options, and its `#id`.
The rail's "What the grill decided" fold is the newest grill notes document's `## Decisions` section rendered by goldmark, counted by its top-level list items, or a link to the document when it has none or its file does not verify.

### Rendering, options and binding

- `RenderMarkdown` is goldmark with its tables extension at its safe defaults: raw HTML omitted and `javascript:`, `vbscript:`, `file:` and `data:` links dropped.
- `OptionMeaning(kind, option)` reads the option-meanings table: a one-line meaning and a `Tone` (positive, negative, carry on) for every option of every set `interp.DecisionOptions`, `render.DecisionOptions`, `store.DecisionOptions` and `cli.StewardDecisionOptions` export; an option it does not describe shows its word alone, neutral.
- `BindAddress(ctx, address, lookup)` resolves a `[web]` address and returns its first address with the port, refusing an address with no host or port, a host that does not resolve, and with `ErrNotTailnet` a host any of whose addresses lies outside `100.64.0.0/10` and `fd7a:115c:a1e0::/48`; `Listen` binds what it returns. `URL(address, repo, task)` is a task page's link.
- `Serve(ln, h)` serves with bounded header, read, write and idle times; `Running.Close` lets requests finish for up to five seconds, then closes them, and returns once stopped.

### Tests

- `page_test.go`: the design-3 fixture (two open asks, three grill rounds, a sent-back review with a fixed finding, a deferred merge, a void superseded acceptance, a two-slice task with slice 1 merged, a Director-run grill) renders, with no identity, every section, anchor, state class, "slice 2 of 2", the void node, a viewport, no `<script>`, no `<form>` and only the pull request links, with the CSP header; it is checked in as `testdata/task.golden.html` (`-update` rewrites it) to open beside design-3; hostile titles, questions, options, words, findings, document names and URLs are escaped; an empty task renders each empty line; the routes' refusals; documents served on a match, refused without a path on a mismatch, a missing file, an unknown name and a climbing record path, Raw as `text/plain`, the frame sandboxed, another task's document only on its own route.
- `markdown_test.go`: tables, nested lists, fenced code and quotes render; raw HTML and the unsafe link schemes are omitted; `FuzzMarkdownIsSafe` asserts no `<script` tag or `on…=` attribute reaches the output; `BindAddress` accepts Tailscale addresses only, IPv4-mapped included, and `Listen` refuses loopback.
- `answer_test.go` (package `web_test`, through `export_test.go`): the Guildmaster's page has a form per ask with its token, subject and enabled tiles, checked in as `testdata/task.answering.golden.html`, and the reader page the same forms; another login, a non-tailnet address and an unknown node see it read-only; an answer and words only reach the fake `Answerer` with the login and redirect, and the redirect shows words only sent; another login, no identity, a bad token, a cross-origin or other-port `Origin`, neither header, `Origin: null` alone, a same-site fetch and a missing option are refused before the `Answerer`, and its refusals, `ErrBusy` and a failure are shown with their status, each with the reason on the ask and the words kept, escaped; a refusal about a gone ask shows atop Now; `TestIdentifierContract` runs `RunIdentifier` against `webtest.Logins` and against `Whois` over a fake `tailscale` the test binary plays; `TestWhoisReal` runs it against the machine's own `tailscale` and is skipped when it is missing or not up.
- `options_test.go` (package `web_test`): every exported option has a meaning and a tone; an unknown option or kind is neutral.
- `internal/cli/web_test.go`: `webSource`'s asks follow Clerk's split before and after an answer and for a void decision, a Director stage, a multi-PR task's slices and merge, `ErrNotFound` and `ErrBusy`; `guildhall web` serves until stopped and refuses no `[web]` and a loopback address; a session's serving record and director status's `web` object; a supervised Director session serves, binds again after a reload and stops at its end, a bind failure only warns, and a Steward session's watchers serve nothing.
- `internal/cli/web_answer_test.go`: over the Clerk fixture with a running session, an answer with CRLF words records its choice and note with `answered_via` and `answered_by`, sends `decision-answered` from the Guildmaster without their words, wakes with sender type `page` and redirects, a double submit replays it, and the story, `decide show` and `clerk inspect` say it was answered on the page; words only send a note from the Guildmaster naming the task and decision, toast it before the wake and leave the decision open; a changed subject, an option not offered, an unknown decision, words over 16 KiB, an answer over 64 KiB, blank words, invalid UTF-8, an answer already given in chat, a void decision and a cancelled task are refused with nothing written, and a held store is 503.
