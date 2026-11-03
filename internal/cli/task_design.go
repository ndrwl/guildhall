package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const taskDesignUsage = `usage: guildhall task design --repo <id> --task <id> --surface web-page|cli|chat|none --brief <file|->
  [--copy <dir> | --store <path> --config <file> --state <dir>]
Add a design round to a feature task after its grill has succeeded and before its first build.
The brief says what the design must show and why; - reads it from standard input. --surface names what the design
changes that the Guildmaster will see, and so the mock-up the designer writes beside design.md: web-page, an HTML
mock-up of the real task page in a desktop and a phone frame; cli or chat, the exact sample output as preformatted
text; none, no mock-up. It is recorded as the brief's first line, "Surface: <surface>", then a blank line and the
brief. A designer Artisan writes the design, and the Guildmaster decides on it in decision design-<n>, opening the
mock-up from the design ask on the task page: approved gives the design to plan, changes_requested reopens the
grill. Replaying the request with an identical surface and brief is safe while its round is open; once the round
is answered, the same request asks for the next round.
Refused for a bug task, once build has started, while a round is open or any attempt runs, and until the grill has
succeeded. Pauses, attempt caps and the dependency gate still apply to the designer.
Examples (run inside the copy):
  guildhall task design --repo app --task t-12 --surface web-page --brief brief.md
Flags: --repo, --task, --surface, --brief are required; --copy, --store, --config, --state default to the selected copy; --help needs no store.`

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
	surfaceFlag := fl.String("surface", "", "what the design changes that the Guildmaster will see: web-page, cli, chat or none (required)")
	if err := fl.Parse(args); err != nil {
		return a.usageError("task design: " + err.Error() + "\n" + taskDesignUsage)
	}
	switch ferr := f.resolve(a); {
	case ferr != nil:
		return a.usageError("task design: " + ferr.Error())
	case fl.NArg() != 0:
		return a.usageError(fmt.Sprintf("task design: unexpected argument %q", fl.Arg(0)))
	case !workflow.ValidID(*task) || *briefFile == "" || *surfaceFlag == "":
		return a.usageError("task design: --task, --surface and --brief are required")
	}
	surface, err := render.ParseSurface(*surfaceFlag)
	if err != nil {
		return a.usageError("task design: --surface: " + err.Error())
	}
	text, err := a.readText("brief", *briefFile, store.MaxTextBytes)
	switch {
	case err != nil:
		return a.usageError("task design: " + err.Error())
	case strings.TrimSpace(text) == "":
		return a.usageError("task design: the brief is empty")
	}
	// The surface is recorded as the brief's first line, so the brief stays one recorded document.
	brief := render.ComposeBrief(surface, text)
	if len(brief) > store.MaxTextBytes {
		return a.usageError(fmt.Sprintf("task design: the brief with its Surface: line is %d bytes, over the %d-byte limit", len(brief), store.MaxTextBytes))
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("task design: %w", err))
	}
	return a.advanced(a.advance(f, interp.Event{Kind: interp.DesignRequested, Repository: f.repo, Task: *task, Reason: brief}))
}
