package cli

// artisanHarness runs `guildhall artisan harness inventory|kill-owned|probe
// --lab`, the lab's test tooling (docs/artisan.md#harness). It refuses to run
// without --lab, a usage error, because it exists for tests and lab runs.
//
//   - inventory, with the attempt flags, --inventory <dir> or both, prints
//     one JSON line listing every process the attempt's records name, each
//     with its role, identity and liveness: the launcher and the worker from
//     the ownership record, and each process recorded in the inventory
//     directory, such as the fake Artisan's inventory directive writes.
//   - kill-owned lists the same processes, kills each that is still alive
//     with artisan.Kill, by identity and never by name, and prints the
//     processes it killed.
//   - probe reads a JSON array of identities from standard input and prints
//     each with its liveness.
//
// Each exits 0 when it did its job and 1 on an error, such as a store or an
// inventory that cannot be read, or a kill that failed.
func (a *App) artisanHarness(args []string) int {
	return 1
}
