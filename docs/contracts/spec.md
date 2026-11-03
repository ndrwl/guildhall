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
`ok` is false for a spec with no block, an empty one or one longer than `SummaryMax`, 4 KiB, so an ask shows its fallback rather than a cut block.
The interpreter calls it to put the block in a plan approval's question, and `internal/web` to show it on the approval ask, read from the verified spec.
`ErrNoSummary` is the error the interpreter's plan check wraps when a spec an approval will show has no usable block.

`OpenQuestions(data, heading)` reads one named section, such as `## Open questions`, into `Questions{None, Items}`, each item a `Question{N, Title, Text}`.
The section runs from a line that is exactly the heading to the next `#` or `##` heading; fenced code blocks are skipped, and CRLF is read as LF.
It is either "None", in any letter case and with an optional full stop, or questions, each starting on a line `**Qn. <title>**`, numbered from 1 with no gaps, and each with a `Recommended:` line outside fenced code; a list or bold marker before a label is allowed.
`Text` is the question's block verbatim, from its `**Qn.` line up to the next question or the section's end, trailing blank lines dropped.
A missing section is `ErrNoOpenQuestions`; an empty section, text before the first question, a numbering gap, a question with no title or one with no `Recommended:` line is `ErrOpenQuestionsShape`, naming the question or line.
`SpecQuestions(data)` is `OpenQuestions(data, "## Open questions")` that also requires an `Options:` line in every question, the spec template's shape.
`Questions.Render(max)` joins the blocks with a blank line between them and, when they exceed `max` bytes, keeps the whole questions that fit and ends with `(cut; read the spec)`.
`Questions.Part()` is the part an approval ask shows after the Summary: `Open questions: none.`, or `Open questions in full:` then `Render(QuestionsMax)`, 16 KiB, a limit of its own so long questions never make the Summary disappear.
`SpecPart(data)` is `SpecQuestions(data)`'s `Part()`, with `ok` false when the spec has no readable section, as with specs recorded before the plan check, so the ask leaves the part out.
The interpreter's question text and `internal/web`'s summary box both call `SpecPart` on the verified spec, so chat and page show the same questions.
A design round's design uses the spec's shape for its `## Open questions`, so the design check, the design ask's question text and its box on the task page call `SpecQuestions` and `SpecPart` on the verified design in the same way.
All of these are pure functions of the bytes, safe to call at once, and a question is identified by its number in its section.

The interpreter calls `Slices` and `CheckRevision` when it records a plan's spec ([decision](../decisions/multi-pr-tasks.md)), and works out the task's slices from the accepted spec with `Slices`.
When a `guildmaster-approval` step of the task's workflow takes that spec as an input, it also refuses, before the spec is first recorded, a spec with no usable Summary block or whose `SpecQuestions` errors: the plan attempt fails with the reason and the planner runs again. A plan no approval shows, such as the bug workflow's, is not checked, and a spec already recorded is not checked again on replay.
It refuses a designer's `design.md` whose `SpecQuestions` errors the same way, before it is first recorded, and a design already recorded is not checked again on replay ([design rounds](../interpreter.md#design-rounds)).
One implementation and no interface, like `digest`.

## Tests

`slices_test.go` table-tests valid lists (none, one, three, a title with a colon, CRLF line ends, a heading inside a fence), every refusal with the entry it names, that parsing twice gives the same result, and `CheckRevision`'s merged-prefix rule: unmerged slices rewritten, added or dropped are accepted; a merged entry's title or body changed, a merged entry dropped, a list removed after a merge and a list with nothing left to deliver are refused.
`summary_test.go` table-tests `Summary`: a block present, none, an empty one, CRLF line ends, the heading inside a fence and a fence inside the block, a block ending at the next `#` or `##` but not at `###`, one at the cap and one a byte over, and a spec with both a Summary and `## Slices` whose `Slices` are unchanged.
`questions_test.go` table-tests `OpenQuestions` and `SpecQuestions`: None, one question, d150 plan-1's three, CRLF line ends, the section ending at the next heading, the heading and a question line inside a fence, bold labels, every refusal with the question or line it names, the hallmark report's `## Questions` heading with an `Alternative:` line, `Render`'s cut at each question boundary, `Part` and `SpecPart`.
The plan check is tested in `internal/interp`'s `summary_test.go`: refused with no Summary, no section, prose, a missing `Recommended:` or `Options:` line or a gap; not checked in a workflow with no approval; not checked again for a spec already recorded.
The engine's use of `Slices` and `CheckRevision` is tested in `internal/interp`'s `slice_test.go`, through the scripted mock agent.
