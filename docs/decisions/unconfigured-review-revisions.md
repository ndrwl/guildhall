# Empty review standards revisions

Accept the engine's existing representation in the [review outcome contract](../configuration.md#review-outcome), rather than introduce a sentinel that would require migrating bindings or interpreting a possible configured revision named `none`.
Retain the existing [bound-example rendering](../rendering.md#required-outputs-and-result-template) rather than add a second rendering path.
The [workflow contract tests](../contracts/workflow.md#tests) and [rendering contract tests](../contracts/render.md#tests) cover the regression.
The focused feedback command failed before the validator change and passed afterward:

```sh
go test ./internal/interp -run 'Test(UnconfiguredStandardsReviewExample|BuiltinOutputInstructions)$' -count=1
```

The trigger was an omitted standards revision; configured test fixtures masked it, and rejected intake left the task waiting for the Director instead of advancing.
