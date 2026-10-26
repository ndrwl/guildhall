# Review

Review the head you were given against the accepted spec and the standards.
Check that it does what the spec says, that its tests prove it and that nothing else changed.
For each problem give the file, the line, what is wrong and what would fix it.
Separate what must change from what you merely suggest.
Do not change the code yourself.
Write the required `outcome` port to `outcome.json` in your working directory and list it in result `outputs` as `{"path":"outcome.json"}`.
Use the strict `guildhall.review-outcome/1` JSON format and the minimal example in the generated Required outputs section, with your actual verdict, findings and dispositions and the exact bound revisions shown there.
The verdict is `approved`, `changes_requested` or `rejected`, not `APPROVE`.
A prose verdict in the result summary alone is not an outcome.
