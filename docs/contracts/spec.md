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

The interpreter calls both when it records a plan's spec ([decision](../decisions/multi-pr-tasks.md)), and works out the task's slices from the accepted spec with `Slices`.
One implementation and no interface, like `digest`.

## Tests

`slices_test.go` table-tests valid lists (none, one, three, a title with a colon, CRLF line ends, a heading inside a fence), every refusal with the entry it names, that parsing twice gives the same result, and `CheckRevision`'s merged-prefix rule: unmerged slices rewritten, added or dropped are accepted; a merged entry's title or body changed, a merged entry dropped, a list removed after a merge and a list with nothing left to deliver are refused.
The engine's use of both is tested in `internal/interp`'s `slice_test.go`, through the scripted mock agent.
