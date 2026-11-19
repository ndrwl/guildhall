# Plan

Write the specification the Guildmaster will accept before anything is built.
Use the context documents as your evidence.
When a `design` input is given, it is the design the Guildmaster approved, and the spec follows it.
Start the spec with a `# ` heading that names the change as a pull request title, followed by a short `## Summary` block (Goal, size, touches, slices, changed since the grill, the number of open questions) and a short `## Goal` section.
Write the size as `- **Size:** about <N> non-test lines in <M> files.`, after `<k> slices, ` when there are slices, counting only lines and files that are not tests.
Write `- **Touches:** none.` unless the change touches secrets, managed settings or trust (who may approve, grant or merge what), and then list the ones it touches, such as `- **Touches:** secrets and trust.`
Guildhall reads these two items: where the repository allows it, the Director may accept a one-slice plan with no open questions that is small enough and touches none of them, so state them honestly; one in another form keeps the plan with the Guildmaster.
State what will change, what will not, how it will be tested and how the Guildmaster will know it is done.
Keep it short enough to read in a few minutes.
List every question that is still open under `## Open questions`, or write "None": each starts on a line `**Qn. <title>**`, numbered from Q1 with no gaps, and has an `Options: (a) <one answer>; (b) <another>.` line and a `Recommended: (<letter>). <why>` line.
When the grill notes hold a `## Questions` list, the grill ran unattended and nobody answered it: carry every question there that your evidence does not settle into `## Open questions`, with the grill's recommendation as yours, since that is how the Guildmaster sees it.
Nothing else may come before the first `**Q1.` line: no sentence, no `### Q1.` heading, no list marker such as `- **Q1.`, and no explanation after "None".
The approval ask shows each question after the Summary with its recommended option first and marked, and Guildhall refuses a spec the Guildmaster will approve that has no Summary block, open questions in another shape, or a `Recommended:` line that does not start with one of its option letters.
When the work is more than one pull request, list its slices under `## Slices`: a numbered list, each entry a bold title, what that pull request delivers and its expected size; Guildhall delivers them in order and refuses a malformed list.
When slices have already merged, keep their entries unchanged and rewrite only the ones still to come.
Write the required `spec` port as a UTF-8 Markdown document at `spec.md` in your working directory.
The prompt's Strict sections block shows a valid and an invalid example of each of these sections, with the error the invalid one gets.
Submit your result with `guildhall result submit`, as the Result template says: it refuses a spec in another shape with the reason intake would give, so fix the spec and submit again until it prints `ok`.
When a merged pull request of this repository already shipped everything the task asks for, write no spec and report `failed` with an `already-delivered` outcome naming it in `by_pr`, as the Result template shows; when the task needs no change at all, report `failed` with a `nothing-to-do` outcome saying why.
Start the summary with `Already delivered by <pr url>` or `Nothing to do`, then give the evidence.
