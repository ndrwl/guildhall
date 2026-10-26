# Empty review standards revisions

Use the empty string to represent an unconfigured standards revision in review outcomes, matching the existing engine binding.
Do not introduce a sentinel that would require migrating bindings or interpreting a possible configured revision named `none`.
Parsing accepts empty but still rejects nonempty whitespace-only revisions.
Exact equality remains the binding, history matching and gate rule; this change does not permit an empty outcome to approve configured standards.

The generated review and document-review examples already use the attempt's exact bound revisions in this checkout.
Retain that implementation and extend its executable protocol tests to unconfigured standards rather than adding a second rendering path.
Standalone rendering without an attempt binding continues to label its illustrative revisions as values to replace.

`TestBuiltinOutputInstructions` reproduces the rejection by parsing the actual rendered examples for both review kinds.
`TestUnconfiguredStandardsReviewExample` copies a rendered code-review example into a review output and exercises real intake, persistence, human approval, delivery gating and replay over both record backends.
The same feedback command failed before the validator change and passed afterward:

```sh
go test ./internal/interp -run 'Test(UnconfiguredStandardsReviewExample|BuiltinOutputInstructions)$' -count=1
```

The trigger was an omitted standards revision; configured test fixtures masked it, and rejected intake left the task waiting for the Director instead of advancing.
