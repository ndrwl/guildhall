# Review

Review the head you were given against the accepted spec and the standards.
Check that it does what the spec says, that its tests prove it and that nothing else changed.
For each problem give the file, the line, what is wrong and what would fix it.
Separate what must change from what you merely suggest.
Do not change the code yourself.
Write the required `outcome` port to `outcome.json` in your working directory.
Use the strict `guildhall.review-outcome/1` JSON format, with your actual verdict, findings and dispositions and the exact bound revisions.
The verdict is `approved`, `changes_requested` or `rejected`, not `APPROVE`.

When your step's id ends in `-rebase-review`, its scope is `resolution`: you are the narrowed review of a rebase Guildhall made, and the `resolution` input is the diff from the conflict-marked replay to its resolution.
Review only those hunks: check that each resolves its conflict, keeps both sides' behaviour and changes nothing else.
Approve with no findings only when the resolution is exactly that; any finding, advisory ones included, reaches the Guildmaster.
