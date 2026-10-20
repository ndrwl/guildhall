# `cmd/guildhall`

## Contract

The process entry point: arguments, standard streams and exit status are handed to `cli.App.Run`.

## Tests

`main_test.go` builds the binary with the release version linker flag and runs `version`, `doctor` and an unknown command
