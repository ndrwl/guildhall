package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

const sliceDeliveredUsage = `usage: guildhall slice delivered --repo <id> --task <id> --slice <k> (--by-slice <j> | --by-pr <url>) --reason <text>
  [--copy <dir> | --store <path> --config <file> --state <dir>]
Close a task's current slice as already delivered by an earlier slice or a merged pull request.
--by-slice names an earlier slice of the task with a merge record; --by-pr a pull request of the task's repository
that the forge reports merged. The slice closes with nothing merged for it: the task moves to the next slice, or is
done after its last. A one-PR task is slice 1 of 1: --slice 1 --by-pr closes it done, and --by-slice is refused.
A slice that is not current or has merged, and a running attempt, are refused.
Prints the task's next wait as task start does; running it again after the close prints where the task waits.
Examples (run inside the copy):
  guildhall slice delivered --repo app --task t-12 --slice 2 --by-slice 1 --reason "Slice 1 shipped every slice 2 item"
  guildhall slice delivered --repo app --task t-12 --slice 3 --by-pr https://github.com/o/app/pull/296 --reason "#296 shipped it"
  guildhall slice delivered --repo app --task t-13 --slice 1 --by-pr https://github.com/o/app/pull/351 --reason "#351 shipped the change"
Flags: --repo, --task, --slice, --reason and one of --by-slice or --by-pr are required; --copy, --store, --config,
--state default to the selected copy; --help needs no store.`

// slice runs `guildhall slice`: the Director's verbs on one slice of a task, a one-PR task's being slice 1 of 1.
func (a *App) slice(args []string) int {
	if len(args) > 0 && args[0] == "delivered" {
		return a.sliceDelivered(args[1:])
	}
	return a.usageError("slice: the command is delivered")
}

// sliceDelivered runs `guildhall slice delivered`: it feeds slice-delivered for the Director's close of a task's
// current slice, or a one-PR task, whose work an earlier slice or a merged pull request already delivered
// (docs/running.md#slice-delivered). It advances the task under its lock, as task start does, and prints its next
// wait. A task the close already finished prints done.
func (a *App) sliceDelivered(args []string) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		if _, err := fmt.Fprintln(a.Stdout, sliceDeliveredUsage); err != nil {
			return 1
		}
		return 0
	}
	fl := flag.NewFlagSet("slice delivered", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var f advanceFlags
	f.register(fl)
	task := fl.String("task", "", "task id")
	slice := fl.String("slice", "", "the current slice, closed as delivered")
	bySlice := fl.String("by-slice", "", "the earlier slice whose merge delivered it")
	byPR := fl.String("by-pr", "", "the URL of the merged pull request that delivered it")
	reason := fl.String("reason", "", "the evidence that the slice's work is delivered")
	if err := fl.Parse(args); err != nil {
		return a.usageError("slice delivered: " + err.Error() + "\n" + sliceDeliveredUsage)
	}
	k, kerr := strconv.Atoi(*slice)
	j, jerr := 0, error(nil)
	if *bySlice != "" {
		j, jerr = strconv.Atoi(*bySlice)
	}
	switch ferr := f.resolve(a); {
	case ferr != nil:
		return a.usageError("slice delivered: " + ferr.Error())
	case fl.NArg() != 0:
		return a.usageError(fmt.Sprintf("slice delivered: unexpected argument %q", fl.Arg(0)))
	case !workflow.ValidID(*task) || kerr != nil || k < 1 || strings.TrimSpace(*reason) == "":
		return a.usageError("slice delivered: --task, a positive --slice and a nonempty --reason are required")
	case (*bySlice == "") == (*byPR == ""):
		return a.usageError("slice delivered: give one of --by-slice or --by-pr")
	case jerr != nil || *bySlice != "" && j < 1:
		return a.usageError("slice delivered: --by-slice takes a positive slice number")
	}
	if err := f.listed(); err != nil {
		return a.fail(fmt.Errorf("slice delivered: %w", err))
	}
	ev := interp.Event{Kind: interp.SliceDelivered, Repository: f.repo, Task: *task, Slice: k, BySlice: j, PullRequest: *byPR, Reason: *reason}
	n, err := a.advance(f, ev)
	if errors.Is(err, interp.ErrTaskClosed) {
		if code, ok := a.deliveredDone(f, *task, k); ok {
			return code
		}
	}
	if err != nil {
		return a.fail(fmt.Errorf("slice delivered: %w", err))
	}
	return a.advanced(n, nil)
}

// deliveredDone prints, for a repeat of a delivery that closed task done, where the task waits, as its last advance
// left it, and reports true; a task closed otherwise reports false.
func (a *App) deliveredDone(f advanceFlags, task string, k int) (int, bool) {
	records := storeRecords{path: f.store, wait: f.wait}
	t, err := records.Task(f.repo, task)
	if err != nil || t.State != store.TaskDone {
		return 0, false
	}
	docs, err := records.Documents(f.repo, task)
	if err != nil {
		return 0, false
	}
	delivered := false
	for _, d := range docs {
		delivered = delivered || d.Name == interp.DeliveryRecord(k)
	}
	if !delivered {
		return 0, false
	}
	state, err := filepath.Abs(f.state)
	if err != nil {
		return 0, false
	}
	data, err := os.ReadFile(filepath.Join(state, f.repo, task, nextFile))
	if err != nil || !strings.Contains(string(data), `"wait":"done"`) {
		return a.printJSON(next{Wait: interp.WaitDone}), true
	}
	if _, err := a.Stdout.Write(data); err != nil {
		return 1, true
	}
	return 0, true
}
