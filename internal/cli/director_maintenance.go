package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// directorMaintenance runs `guildhall director maintenance record|list` (docs/director.md#maintenance).
func (a *App) directorMaintenance(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "record":
			return a.directorMaintenanceRecord(args[1:])
		case "list":
			return a.directorMaintenanceList(args[1:])
		}
	}
	return a.usageError("director maintenance: the commands are record and list")
}

// directorMaintenanceRecord runs `guildhall director maintenance record --notes --summary <text> [--copy <dir> |
// --store <file>]`: it records the note audit as the newest session's and prints "run <k> recorded". --hallmark,
// with the --repo and --task it took, is refused: a hallmark is recorded when its task finishes
// (docs/decisions/engine-owned-hallmark-cadence.md).
func (a *App) directorMaintenanceRecord(args []string) int {
	const name = "director maintenance record"
	var notes, hallmark bool
	var repo, task, summary string
	path, _, code := a.sessionArgs(name, args, false, func(fl *flag.FlagSet) {
		fl.BoolVar(&notes, "notes", false, "record a note audit")
		fl.BoolVar(&hallmark, "hallmark", false, "refused: "+store.HallmarkRecordRetired)
		fl.StringVar(&repo, "repo", "", "refused, with --hallmark")
		fl.StringVar(&task, "task", "", "refused, with --hallmark")
		fl.StringVar(&summary, "summary", "", "one line: what was removed and what was proposed")
	})
	if code >= 0 {
		return code
	}
	switch {
	case hallmark:
		return a.usageError(name + ": " + store.HallmarkRecordRetired)
	case !notes:
		return a.usageError(name + ": give --notes")
	case repo != "" || task != "":
		return a.usageError(name + ": a note audit takes no --repo or --task")
	case summary == "":
		return a.usageError(name + ": --summary is required")
	}
	run := store.MaintenanceRun{Kind: store.MaintenanceNotes, Summary: summary}
	got, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.MaintenanceRun, error) {
		d, err := s.LatestDirectorSession()
		if errors.Is(err, store.ErrNotFound) {
			return store.MaintenanceRun{}, errors.New("no Director session; start one with guildhall director start")
		} else if err != nil {
			return store.MaintenanceRun{}, err
		}
		run.Session = d.Number
		got, _, err := s.RecordMaintenance(run)
		return got, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "run %d recorded\n", got.Sequence); err != nil {
		return 1
	}
	return 0
}

// directorMaintenanceList runs `guildhall director maintenance list [--json] [--copy <dir> | --store <file>]`: every
// recorded run, oldest first, one line each, or "No maintenance runs.".
func (a *App) directorMaintenanceList(args []string) int {
	var asJSON bool
	path, _, code := a.sessionArgs("director maintenance list", args, false, func(fl *flag.FlagSet) {
		fl.BoolVar(&asJSON, "json", false, "print JSON")
	})
	if code >= 0 {
		return code
	}
	runs, err := withStore(path, true, func(s *store.Store) ([]store.MaintenanceRun, error) { return s.MaintenanceRuns() })
	if err != nil {
		return a.fail(fmt.Errorf("director maintenance list: %w", err))
	}
	if asJSON {
		return a.printJSON(runs)
	}
	out := "No maintenance runs.\n"
	if len(runs) > 0 {
		var lines []string
		for _, r := range runs {
			lines = append(lines, maintenanceLine(r))
		}
		out = strings.Join(lines, "\n") + "\n"
	}
	if _, err := fmt.Fprint(a.Stdout, out); err != nil {
		return 1
	}
	return 0
}

// maintenanceLine is one run as list prints it: "<k> s<session> <time> notes: <summary>", or "hallmark
// <repo>/<task>" in place of "notes".
func maintenanceLine(r store.MaintenanceRun) string {
	what := string(r.Kind)
	if r.Kind == store.MaintenanceHallmark {
		what += " " + r.Repository + "/" + r.Task
	}
	return fmt.Sprintf("%d s%d %s %s: %s", r.Sequence, r.Session, r.Time.UTC().Format(time.RFC3339), what, r.Summary)
}
