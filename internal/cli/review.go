package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ndrwl/guildhall/internal/workflow"
)

// review runs a review subcommand. check lets a reviewer validate its own outcome file before it finishes, with
// the checks intake runs on it: the strict decode, /1 normalization and Validate, and with --previous whether the
// outcome dispositions every finding of the previous outcome. It does not check the binding to the attempt's
// revisions, which the reviewer has no file for. It needs no store, copy or config.
// It prints ok and exits 0 for a valid outcome, writes every problem to stderr and exits 1 for a refused or
// unreadable one, and exits 2 on a usage error.
func (a *App) review(args []string) int {
	if len(args) == 0 {
		return a.usageError("missing review subcommand")
	}
	if args[0] != "check" {
		return a.usageError(fmt.Sprintf("unknown review subcommand %q", args[0]))
	}
	flags := flag.NewFlagSet("review check", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	previous := flags.String("previous", "", "the previous outcome of the same review step")
	if err := parseInterspersed(flags, args[1:]); err != nil {
		return a.usageError("review check: " + err.Error())
	}
	if flags.NArg() != 1 {
		return a.usageError("review check requires one <outcome-file>")
	}
	o, err := readReviewOutcome(flags.Arg(0))
	if err != nil {
		return a.fail(err)
	}
	if *previous != "" {
		prev, err := readReviewOutcome(*previous)
		if err != nil {
			return a.fail(fmt.Errorf("previous: %w", err))
		}
		if err := o.Covers(*prev); err != nil {
			return a.fail(fmt.Errorf("%s: %w", flags.Arg(0), err))
		}
	}
	if _, err := fmt.Fprintln(a.Stdout, "ok"); err != nil {
		return 1
	}
	return 0
}

// readReviewOutcome reads and parses a review outcome file as intake does.
func readReviewOutcome(path string) (*workflow.ReviewOutcome, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	o, err := workflow.ParseReviewOutcome(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}
