# Task list browser check (d222, slice 3)

The spec's slice 3 browser check of `list.js`, run with chrome-devtools-axi (headless Chrome) against the checked-in fixture server:

```text
go test ./internal/web -run TestListBrowserFixture -list-fixture 127.0.0.1:8222 -timeout 0
```

It was run at 1280 × 900 and at 390 × 844 (mobile, touch, DPR 3), each in light and dark themes (`emulate --color-scheme`).
The checks were done by script in the page (`eval`), and the screenshots are in `list-page-browser-check-2026-11-16/`.

## Red: list.js never ran

On the first pass no `/list.json` request was made at all, and nothing changed after `/fixture/change`.
`list.js` is inlined in `<head>`, so when it ran `main.tri` did not exist yet, and it returned at its first guard.
None of the live behaviour (refresh, tint, ticking times, error line, phone fold, in-browser sort and filter) had ever worked.
The fix is that `list.js` waits for `DOMContentLoaded` while the document is loading. `TestListScriptWaitsForTheList` fails without it.

The second pass found that every refresh re-attached every row with `replaceChildren`, so a focused link in an unchanged row lost the keyboard focus.
`list.js` now moves only rows that are out of place (`arrange`).

## Green: results after the fixes

| Check | 1280 light | 1280 dark | 390 light | 390 dark |
|---|---|---|---|---|
| `/fixture/change`: d215 reads "(change n)", attempt +1, PR #390 ●, tinted (`r tint`), and is the only row replaced | pass | pass | — | — |
| The tint fades: about 2.5 s later the class is `r` and the background is going back to none | pass | — | — | — |
| A focused link in an unchanged row (d211) keeps the focus through a refresh | pass | — | — | — |
| Held folded, filter `d2` and sort PR survive a refresh; URL `/?q=d2&sort=pr` | pass | — | PR chip: pass | — |
| A reload of that URL keeps the filter and sort (Held opens again, as the server draws it) | pass | — | — | — |
| t01 reads "now", then about 1 min later "1 min ago" with no reload and the same element and title | pass | — | — | — |
| t01–t11 in the browser match the server's text exactly | pass | — | — | — |
| Every `<time>` has `datetime` and `title` | pass | — | — | — |
| `/fixture/fail`: all 25 rows stay and "Not refreshed since …" shows; after `/fixture/ok` it clears within 60 s | pass | — | 503: pass | — |
| The stats strip is closed to its one-line summary | — | — | pass | pass |
| Nothing scrolls sideways (`scrollWidth` = `clientWidth`, no element past the right edge) | pass | pass | pass | pass |

The step words were: now, now, 1 min ago, 58 min ago, 1 hr ago, 23 hr ago, 1 day ago, 2 days ago, 30 days ago, 15 Oct, 12 Oct 2025.
A dash means the check is not about width or theme, and it was not repeated there.

## Against Layout A

The mock-up file itself was not available to this run. The screenshots were compared with Layout A as the spec describes it:

- one column up to 1240 px wide;
- the dark band with "3 tasks wait for you" in amber;
- the stats strip;
- Needs you pinned in amber, each row with its ask line;
- the filter bar with the priority and repository chips;
- the column header;
- the stage groups, each with a count, a status line and a blue bar;
- Recently finished collapsed;
- at 390 px, rows on three lines and the strip as one line.

The screenshots match that description in both themes. Someone holding the mock-up should still set them side by side with its frames.

## Screenshots

- `1280-light.png`, `1280-dark.png`: the list as loaded.
- `1280-light-tint-filter-sort.png`: d215 tinted after a change, with filter `d2`, sort PR and Held folded (taken before the focus fix, which does not change the picture).
- `1280-dark-tint.png`: d215 tinted in the dark theme.
- `1280-light-503.png`, `390-light-503.png`: the error line under the band with the rows kept.
- `390-light.png`, `390-dark.png`: the phone layout with the strip closed.

## Not covered here

`poll.js` is also inlined in `<head>` on the task and reader pages and looks up `#newbar` at once, so it probably never runs either.
That code predates d222. It was not exercised here (the fixture serves no task page) and is left for its own task.
