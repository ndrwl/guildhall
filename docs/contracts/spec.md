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

Each question is also read into its parts, failing closed (`Question.Shaped`): `Body`, its lines before its first label; `Options`, the letters and texts of an `Options: (a) …; (b) ….` line, split only at a `; (x) ` where x is the next letter, so option text may hold `;`, parentheses and inline Markdown; `Recommended`, the letter a `Recommended:` line starts with, as `(b)`, `b)` or `b.`, and `Why`, the rest of that line; or, on a hallmark question with no options, `Why` as the whole `Recommended:` line and `Alternative` as its `Alternative:` line; and `More`, its lines after a blank line.
A label's line continues on the lines straight after it, up to a blank line or the next label.
A question with a fenced block, text after its title, a label twice, a label after its other lines, options that are not consecutive letters from (a) with text, fewer than two, or a `Recommended:` line that does not start with one of their letters keeps only `N`, `Title` and `Text`: nothing is half read or dropped, and it shows word for word.
`Question.Markdown()` is the question as an ask's text writes it: when shaped, `**Qn. title**`, the body, the recommended option first as `- ★ **(b) text** · Recommended. why`, the others in letter order as `- (a) text`, or `- ★ **Recommended:** …` and `- Alternative: …`, then its other lines; otherwise `Text`. Letters are never renumbered, so "Q2 (a)" means the same option wherever it shows.

`ReadSpecQuestions(data)` is `OpenQuestions(data, "## Open questions")` that also requires an `Options:` line in every question, the spec template's shape; it is lenient about the `Recommended:` line, so documents recorded before the letter rule still read.
`SpecQuestions(data)`, which the plan and design checks call, also refuses a question that breaks the letter rule, naming it and the reason: its `Options:` line must split into lettered options `(a) …; (b) …`, and its `Recommended:` line must start with one of their letters, so its marker cannot quietly disappear. Only that rule refuses; a question that keeps it but does not read as a card for another reason, such as a fenced block or text after its title, is accepted and shows word for word.
`Questions.Render(max)` joins the blocks (`Markdown()`) with a blank line between them and, when they exceed `max` bytes, keeps the whole questions that fit (`Fit(max)`) and ends with `(cut; read the spec)`.
`Questions.Part()` is the part an approval ask shows after the Summary: `Open questions: none.`, or `**Open questions · n**`, a blank line, then `Render(QuestionsMax)`, 16 KiB, a limit of its own so long questions never make the Summary disappear. `PlanPart()` adds a blank line and `Reply **approve** to take every recommendation, or name the answers to change, such as "Q2 (a)".` after questions.
`SpecPart(data)` and `PlanPart(data)` are `ReadSpecQuestions(data)`'s `Part()` and `PlanPart()`, with `ok` false when the spec has no readable section, as with specs recorded before the plan check, so the ask leaves the part out.
The interpreter's plan approval text calls `PlanPart` on the verified spec, and `internal/web`'s summary box draws its cards from `ReadSpecQuestions` on it, the same parse, so chat and page show the same questions, in the same order, with the same marker.
A design round's design uses the spec's shape for its `## Open questions`, so the design check calls `SpecQuestions`, and the design ask's question text (`SpecPart`) and its cards on the task page (`ReadSpecQuestions`) read the verified design in the same way.
`Questions.PartWithin(max)` is `Part()` kept within `max` bytes: when it does not fit, the heading and the whole questions that fit, ending with `(cut; read the spec)`, with `ok` false when `max` has no room for the heading and the marker, or for the none line.
`ReportQuestions(data)` reads a hallmark report's `## Questions` (`OpenQuestions(data, "## Questions")`, with `Recommended:` and `Alternative:` lines and no `Options:` rule), its text made valid UTF-8 first; `ReportPart(data, max)` returns its `PartWithin(max)`, with `ok` false when the report has no readable section. The follow-ups question text calls `ReportPart` and the follow-ups ask's cards on the task page `ReportQuestions`, on the verified report.
`OldQuestionsInFull` (`Open questions in full:`) and `OldSummaryHeader` (`Plan summary:`, beside today's `SummaryHeader`, `**Summary**`) are the headings of asks recorded before d153, whose stored text is immutable; the task page still recognises them.
All of these are pure functions of the bytes, safe to call at once, and a question is identified by its number in its section.

The interpreter calls `Slices` and `CheckRevision` when it records a plan's spec ([decision](../decisions/multi-pr-tasks.md)), and works out the task's slices from the accepted spec with `Slices`.
When a `guildmaster-approval` step of the task's workflow takes that spec as an input, it also refuses, before the spec is first recorded, a spec with no usable Summary block or whose `SpecQuestions` errors: the plan attempt fails with the reason and the planner runs again. A plan no approval shows, such as the bug workflow's, is not checked, and a spec already recorded is not checked again on replay.
It refuses a designer's `design.md` whose `SpecQuestions` errors the same way, before it is first recorded, and a design already recorded is not checked again on replay ([design rounds](../interpreter.md#design-rounds)).
One implementation and no interface, like `digest`.

## Tests

`slices_test.go` table-tests valid lists (none, one, three, a title with a colon, CRLF line ends, a heading inside a fence), every refusal with the entry it names, that parsing twice gives the same result, and `CheckRevision`'s merged-prefix rule: unmerged slices rewritten, added or dropped are accepted; a merged entry's title or body changed, a merged entry dropped, a list removed after a merge and a list with nothing left to deliver are refused.
`summary_test.go` table-tests `Summary`: a block present, none, an empty one, CRLF line ends, the heading inside a fence and a fence inside the block, a block ending at the next `#` or `##` but not at `###`, one at the cap and one a byte over, and a spec with both a Summary and `## Slices` whose `Slices` are unchanged.
`questions_test.go` table-tests `OpenQuestions`, `ReadSpecQuestions` and `SpecQuestions`: None, one question, d150 plan-1's three, CRLF line ends, the section ending at the next heading, the heading and a question line inside a fence, bold labels, every refusal with the question or line it names, the letter rule refusing a free-text `Recommended:` while the lenient reader shows it word for word, the hallmark report's `## Questions` heading with an `Alternative:` line, `Render`'s cut at each question boundary, `Part`, `PlanPart`, `SpecPart` and `PlanPart(data)`, `PartWithin`'s cut and refusal at each limit, and `ReportPart` with `Alternative:` lines, CRLF, None, no section, a malformed one, text that is not UTF-8 and a cut.
`TestShape` table-tests the parts: plain, list and bold labels, option text with `;` and parentheses, a recommended letter that is not first, continued lines and other lines, a hallmark question, and each fallback (a free-text or unknown letter, letters that skip, one option, a label twice or after other lines, a fence, text after the title, options with an alternative).
The plan check is tested in `internal/interp`'s `summary_test.go`: refused with no Summary, no section, prose, a missing `Recommended:` or `Options:` line, a `Recommended:` line with no option letter, or a gap; not checked in a workflow with no approval; not checked again for a spec already recorded.
The engine's use of `Slices` and `CheckRevision` is tested in `internal/interp`'s `slice_test.go`, through the scripted mock agent.
