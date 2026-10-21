# `internal/adapter/adaptertest`

## Contract

`RunConformance`: the launch and result protocol every runtime adapter passes through a real child process, with no model calls; `Runtime` is the narrow interface it consumes.
`Subject.Start` runs the command the way a launch path does, such as inside the Artisan launcher, as a `Process` the suite can wait for and stop; nil starts a direct child process.

## Tests

Run by `internal/adapter/fake` and `internal/adapter`
