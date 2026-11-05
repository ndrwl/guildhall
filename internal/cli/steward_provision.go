package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/steward"
)

// provisionApplyTimeout bounds one provisioning apply: its doctor runs, fixes, clones, copies, start, commit and
// push together.
const provisionApplyTimeout = 90 * time.Minute

// stewardProvisionCmd runs `guildhall steward provision`: --dry-run plans a new managed Director copy from a request
// file on a roster machine that already has Guildhall and writes the plan, and --apply carries out an approved plan,
// resuming at its first step not done (docs/steward.md#provisioning).
func (a *App) stewardProvisionCmd(args []string) int {
	const name = "steward provision"
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "")
	request := fs.String("request", "", "")
	planDir := fs.String("plan", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	apply := fs.Bool("apply", false, "")
	approve := fs.String("approve", "", "")
	err := fs.Parse(args)
	switch {
	case err != nil:
	case fs.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case *dryRun == *apply:
		err = errors.New("give exactly one of --dry-run and --apply")
	case *cfgPath == "" || *planDir == "":
		err = errors.New("--config and --plan are required")
	case *dryRun && *request == "":
		err = errors.New("--dry-run needs --request <file>")
	case *apply && *request != "":
		err = errors.New("--request belongs to --dry-run; --apply reads it from the plan")
	case !*apply && *approve != "":
		err = errors.New("--approve applies only with --apply")
	}
	if err != nil {
		_, _ = fmt.Fprintf(a.Stderr, "guildhall: %s: %v\n\n%s", name, err, stewardUsage)
		return 2
	}
	s, err := a.loadSetup(*cfgPath)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), provisionApplyTimeout)
	defer cancel()
	if *apply {
		return a.applySetup(ctx, name, steward.SetupKindProvision, s, *cfgPath, *planDir, *approve)
	}
	data, err := os.ReadFile(*request)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	abs, err := filepath.Abs(*request)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	req, err := steward.ParseProvisionRequest(data, filepath.Dir(abs))
	if err != nil {
		return a.fail(fmt.Errorf("%s: %s: %w", name, *request, err))
	}
	p, err := s.PlanProvision(ctx, req, *planDir)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if err := os.MkdirAll(p.PlanDir, 0o700); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if _, err := io.WriteString(a.Stdout, renderSetupPlan(p, *cfgPath)); err != nil {
		return 1
	}
	return 0
}

// orNone is items joined with commas, or "none".
func orNone(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, ", ")
}
