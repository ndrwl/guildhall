package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/store"
)

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
	const usage = "artisan harness: the commands are inventory, kill-owned and probe, with --lab"
	if len(args) == 0 || !slices.Contains([]string{"inventory", "kill-owned", "probe"}, args[0]) {
		return a.usageError(usage)
	}
	cmd := args[0]
	fl := flag.NewFlagSet("artisan harness "+cmd, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	lab := fl.Bool("lab", false, "acknowledge that this is lab test tooling")
	var f attemptFlags
	var inventory string
	if cmd != "probe" {
		f.register(fl)
		fl.StringVar(&inventory, "inventory", "", "inventory directory")
	}
	if err := fl.Parse(args[1:]); err != nil {
		return a.usageError("artisan harness " + cmd + ": " + err.Error())
	}
	switch {
	case fl.NArg() > 0:
		return a.usageError(fmt.Sprintf("artisan harness %s: unexpected argument %q", cmd, fl.Arg(0)))
	case !*lab:
		return a.usageError("artisan harness " + cmd + " is lab test tooling: pass --lab")
	case cmd == "probe":
		var ids []artisan.Identity
		if err := json.NewDecoder(a.Stdin).Decode(&ids); err != nil {
			return a.fail(fmt.Errorf("artisan harness probe: identities on standard input: %w", err))
		}
		out := make([]ownedProcess, 0, len(ids))
		for _, id := range ids {
			out = append(out, probed("probed", id))
		}
		return a.printJSON(out)
	}
	var own *store.Ownership
	if f != (attemptFlags{}) {
		at, err := f.parse(a.Getenv)
		if err != nil {
			return a.usageError("artisan harness " + cmd + ": " + err.Error())
		}
		if own, err = ownershipOf(at.Store, at.Key); err != nil {
			return a.fail(err)
		}
	} else if inventory == "" {
		return a.usageError("artisan harness " + cmd + ": name the attempt, --inventory or both")
	}
	procs, err := ownedProcesses(own, inventory)
	if err != nil {
		return a.fail(err)
	}
	if cmd == "inventory" {
		return a.printJSON(procs)
	}
	killed := []ownedProcess{}
	var errs []error
	for _, p := range procs {
		if p.Liveness != artisan.Alive.String() {
			continue
		}
		if err := artisan.Kill(p.Identity); err != nil {
			errs = append(errs, fmt.Errorf("kill %s %v: %w", p.Role, p.Identity, err))
			continue
		}
		killed = append(killed, p)
	}
	if code := a.printJSON(killed); code != 0 {
		return code
	}
	if err := errors.Join(errs...); err != nil {
		return a.fail(err)
	}
	return 0
}

// ownedProcess is one process an attempt's records name, with its liveness.
type ownedProcess struct {
	Role string `json:"role"`
	artisan.Identity
	Liveness string `json:"liveness"`
}

func probed(role string, id artisan.Identity) ownedProcess {
	lv, _ := artisan.Probe(id)
	return ownedProcess{Role: role, Identity: id, Liveness: lv.String()}
}

// ownershipOf reads the attempt's ownership record, nil when it has none.
func ownershipOf(path string, k artisan.AttemptKey) (*store.Ownership, error) {
	s, err := store.Open(path, store.Options{ReadOnly: true, RequireStore: true})
	if err != nil {
		return nil, err
	}
	o, err := s.Ownership(k.Repository, k.TaskID, k.Step, k.Attempt)
	err = errors.Join(err, s.Close())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return &o, err
}

// ownedProcesses lists the launcher and worker own names and every process
// recorded in the inventory directory dir, each identity once.
func ownedProcesses(own *store.Ownership, dir string) ([]ownedProcess, error) {
	out := []ownedProcess{}
	add := func(role string, id artisan.Identity) {
		if id.PID == 0 || slices.ContainsFunc(out, func(p ownedProcess) bool { return p.Identity == id }) {
			return
		}
		out = append(out, probed(role, id))
	}
	if own != nil {
		add("launcher", artisan.Identity(own.LauncherIdentity))
		add("worker", artisan.Identity(own.Containment.Worker))
	}
	if dir == "" {
		return out, nil
	}
	recs, err := artisan.ReadProcessRecords(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range recs {
		add(e.Role, e.Identity)
	}
	return out, nil
}
