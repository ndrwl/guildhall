---
status: accepted
date: 2026-10-31
topic: dependencies
tasks: d105, d122
---

# The task pages render Markdown documents with goldmark and its tables extension at safe defaults

## Context

The Guildmaster's [task pages](../web.md) show each recorded document on a reader page ([d122](../decisions/task-page.md)).
Artisans write full GitHub-flavoured Markdown: tables, nested lists and fenced code.
On d105's grill the Guildmaster asked "should we use a markdown rendering library?" and answered "q5 accept" to goldmark.

## Decision

Add `github.com/yuin/goldmark` v1.8.7 for `internal/web`, following the policy of [adding a dependency when a feature needs it](standard-library-only-for-the-foundation.md).
`web.RenderMarkdown` uses one goldmark with only its tables extension, at goldmark's safe defaults: raw HTML is omitted (no `html.WithUnsafe`) and `javascript:`, `vbscript:`, `file:` and `data:` links are dropped.
Its output is the only HTML a page passes to `html/template` unescaped; every other field is escaped, an HTML document is shown only in a sandboxed frame, and every document keeps a Raw link.
`internal/web`'s tests pin that raw HTML and those links render harmless, and a fuzz test that no `<script` tag or `on…=` attribute reaches the output ([contract](../contracts/web.md)).

## Alternatives rejected

- A small renderer written in the repository: it would get GitHub-flavoured Markdown's tables, nested lists and fences subtly wrong, and its safety would be ours to prove.
- Showing documents as plain text: correct but hard to read on a phone, the case the pages exist for.

## Consequences

goldmark is pure Go with no dependencies of its own, so release builds stay `CGO_ENABLED=0`; it is one more module to keep current.
Code blocks have no syntax colours, since goldmark adds none and the pages add no highlighter.
