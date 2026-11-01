# `internal/spec`

## Contract

`Slices(data)` reads an accepted spec's `## Slices` section, the slice list of a [multi-PR task](../tasks.md#multi-pr-tasks), and returns `[]Slice{Index, Title, Body}`.
The section runs from a line that is exactly `## Slices` to the next `#` or `##` heading, and fenced code blocks are skipped, so a heading quoted in one is not the section.
Each entry is a numbered item `1.`, `2.`, … with no gaps, starting with a bold title, `**Title**` or `**Title.**`, then a body, what the slice delivers and its expected size, that runs on over its continuation lines; a colon after the title is dropped.
A spec with no section returns nil, a one-slice task.
An empty section, a line that is no item before the first, an entry numbered out of order, an entry with no bold title, an empty body or a title another entry has, in any letter case, is refused with an error wrapping `ErrSlices` that names the entry.
Parsing is a pure function of the bytes: no file is read and nothing is written, and a slice is identified by its index, its title being display text.

`CheckRevision(prev, next, merged)` refuses a revised spec's slices, `next`, unless they keep the first `merged` entries of the accepted spec's, `prev`, with the same title and body, and still list a slice after them; a spec with no list counts as one slice, and `merged` of 0 accepts anything.
The error wraps `ErrSlices` and names the merged slice changed or dropped.

`Summary(data)` reads a spec's `## Summary` block, the short Goal, Size, Slices, Changed since the grill and Open questions a plan approval ask shows, and returns it with `ok`.
The block runs from a line that is exactly `## Summary` to the next `#` or `##` heading; fenced code blocks are skipped, CRLF is read as LF and the text is trimmed.
`ok` is false for a spec with no block, an empty one or one longer than `SummaryMax`, 4 KiB, so an ask shows its fallback rather than a cut block; a spec is never refused for lacking one.
The interpreter calls it to put the block in a plan approval's question, and `internal/web` to show it on the approval ask, read from the verified spec.

The interpreter calls `Slices` and `CheckRevision` when it records a plan's spec ([decision](../decisions/multi-pr-tasks.md)), and works out the task's slices from the accepted spec with `Slices`.
One implementation and no interface, like `digest`.

## Tests

`slices_test.go` table-tests valid lists (none, one, three, a title with a colon, CRLF line ends, a heading inside a fence), every refusal with the entry it names, that parsing twice gives the same result, and `CheckRevision`'s merged-prefix rule: unmerged slices rewritten, added or dropped are accepted; a merged entry's title or body changed, a merged entry dropped, a list removed after a merge and a list with nothing left to deliver are refused.
`summary_test.go` table-tests `Summary`: a block present, none, an empty one, CRLF line ends, the heading inside a fence and a fence inside the block, a block ending at the next `#` or `##` but not at `###`, one at the cap and one a byte over, and a spec with both a Summary and `## Slices` whose `Slices` are unchanged.
The engine's use of `Slices` and `CheckRevision` is tested in `internal/interp`'s `slice_test.go`, through the scripted mock agent.
