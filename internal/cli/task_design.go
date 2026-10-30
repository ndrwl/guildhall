package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskDesignUsage = `usage: guildhall task design --repo <id> --task <id> --brief <file|->
  [--copy <dir> | --store <path> --config <file> --state <dir>]
Add a design round to a feature task after its grill has succeeded and before its first build.
The brief says what the design must show and why; - reads it from standard input. A designer Artisan writes the
design, and the Guildmaster decides on it in decision design-<n>: approved gives it to plan, changes_requested
reopens the grill. Replaying the request with an identical brief is safe while its round is open; once the round
is answered, the same brief asks for the next round.
Refused for a bug task, once build has started, while a round is open or any attempt runs, and until the grill has
succeeded. Pauses, attempt caps and the dependency gate still apply to the designer.
Examples (with GUILDHALL_COPY set):
  guildhall task design --repo app --task t-12 --brief brief.md
Flags: --repo, --task, --brief are required; --copy, --store, --config, --state default to the selected copy; --help needs no store.`

// taskDesign records the Director's request for a design round with its brief and advances the task, under the
// normal task lock and admission gates (docs/tasks.md#design-rounds).
func (a *App) taskDesign(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(a.Stdout, taskDesignUsage); err != nil {
			return 1
		}
		return 0
	}
	fl := flag.NewFlagSet("task design", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	briefFile := fl.String("brief", "", "file holding the brief, - for standard input (required)")
	if err := fl.Parse(args); err != nil {
		return a.usageError("task design: " + err.Error() + "\n" + taskDesignUsage)
	}
	switch ferr := f.resolve(a.Getenv); {
	case ferr != nil:
		return a.usageError("task design: " + ferr.Error())
	case fl.NArg() != 0:
		return a.usageError(fmt.Sprintf("task design: unexpected argument %q", fl.Arg(0)))
	case !workflow.ValidID(*task) || *briefFile == "":
		return a.usageError("task design: --task and --brief are required")
	}
	brief, err := a.readText("brief", *briefFile, store.MaxTextBytes)
	switch {
	case err != nil:
		return a.usageError("task design: " + err.Error())
	case strings.TrimSpace(brief) == "":
		return a.usageError("task design: the brief is empty")
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("task design: %w", err))
	}
	return a.advanced(a.advance(f, interp.Event{Kind: interp.DesignRequested, Repository: f.repo, Task: *task, Reason: brief}))
}
