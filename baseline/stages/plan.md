# Plan

Write the specification the Guildmaster will accept before anything is built.
Use the context documents as your evidence.
When a `design` input is given, it is the design the Guildmaster approved, and the spec follows it.
Start the spec with a `# ` heading that names the change as a pull request title, followed by a short `## Summary` block (Goal, size, slices, changed since the grill, open questions with recommendations) and a short `## Goal` section.
State what will change, what will not, how it will be tested and how the Guildmaster will know it is done.
Keep it short enough to read in a few minutes, and list any question that is still open.
When the work is more than one pull request, list its slices under `## Slices`: a numbered list, each entry a bold title, what that pull request delivers and its expected size; Guildhall delivers them in order and refuses a malformed list.
When slices have already merged, keep their entries unchanged and rewrite only the ones still to come.
Write the required `spec` port as a UTF-8 Markdown document at `spec.md` in your working directory.
