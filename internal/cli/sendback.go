package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const sendBackUsage = `usage: guildhall step send-back --repo <id> --task <id> --step <id> --attempt <n> --feedback <instructions>
  [--copy <dir> | --store <path> --config <file> --state <dir>]
Start a new attempt of a succeeded Artisan step with Director feedback.
--attempt identifies the succeeded attempt, not the new one; replay with identical feedback is safe.
A running attempt anywhere in the task refuses a new request. Pauses and attempt caps still apply.
Examples (with GUILDHALL_COPY set):
  guildhall step send-back --repo app --task t-12 --step build --attempt 1 --feedback "Resolve the base conflict"
  guildhall step send-back --repo app --task t-12 --step build --attempt 2 --feedback "Revise the implementation"
Flags: --repo, --task, --step, --attempt, --feedback are required; --copy, --store, --config, --state default to the selected copy; --help needs no store.`

// stepSendBack starts a new attempt of a succeeded Artisan step with the
// Director's required feedback, under the normal task lock and admission gates.
func (a *App) stepSendBack(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(a.Stdout, sendBackUsage)
		if err != nil {
			return 1
		}
		return 0
	}
	fl := flag.NewFlagSet("step send-back", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	step := fl.String("step", "", "earlier Artisan step id")
	attempt := fl.String("attempt", "", "succeeded attempt number (replay identity)")
	feedback := fl.String("feedback", "", "Director instructions for the new attempt (required)")
	if err := fl.Parse(args); err != nil {
		return a.usageError("step send-back: " + err.Error() + "\n" + sendBackUsage)
	}
	n, err := strconv.Atoi(*attempt)
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("step send-back: " + ferr.Error())
	case fl.NArg() != 0:
		return a.usageError(fmt.Sprintf("step send-back: unexpected argument %q", fl.Arg(0)))
	case !workflow.ValidID(*task) || !workflow.ValidID(*step) || err != nil || n < 1 || strings.TrimSpace(*feedback) == "":
		return a.usageError("step send-back: --task, --step, positive --attempt and nonempty --feedback are required")
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("step send-back: %w", err))
	}
	return a.advanced(a.advance(f, interp.Event{Kind: interp.StepSentBack, Repository: f.repo, Task: *task, Step: *step, Attempt: n, Reason: *feedback}))
}
