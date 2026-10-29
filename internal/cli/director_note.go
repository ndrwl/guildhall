package cli

import (
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/store"
)

// directorNote runs `guildhall director note add|edit|remove|list` (docs/director.md#notes). Each change prints a
// one-line receipt, such as "n7 added", and list prints the live notes as startup.md does.
func (a *App) directorNote(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "add":
			return a.directorNoteAdd(args[1:])
		case "edit":
			return a.directorNoteEdit(args[1:])
		case "remove":
			return a.directorNoteRemove(args[1:])
		case "list":
			return a.directorNoteList(args[1:])
		}
	}
	return a.usageError("director note: the commands are add, edit, remove and list")
}

// directorNoteAdd runs `guildhall director note add --kind <kind> [--task <repo>/<id>] <text> [--copy <dir> |
// --store <file>]`.
func (a *App) directorNoteAdd(args []string) int {
	var kind, task string
	path, _, ops, code := a.sessionOperands("director note add", args, false, []string{"<text>"}, func(fl *flag.FlagSet) {
		fl.StringVar(&kind, "kind", "", "promised, guildmaster or plan")
		fl.StringVar(&task, "task", "", "the task the note is about, <repo>/<id>")
	})
	if code >= 0 {
		return code
	}
	if kind == "" {
		return a.usageError("director note add: --kind is required: promised, guildmaster or plan")
	}
	return a.noteChange("director note add", path, "added", func(s *store.Store, n int) (store.Note, error) {
		note, _, err := s.AddNote(n, store.NoteKind(kind), task, ops[0])
		return note, err
	})
}

// directorNoteEdit runs `guildhall director note edit <id> <text> [--copy <dir> | --store <file>]`.
func (a *App) directorNoteEdit(args []string) int {
	path, _, ops, code := a.sessionOperands("director note edit", args, false, []string{"<id>", "<text>"}, nil)
	if code >= 0 {
		return code
	}
	return a.noteChange("director note edit", path, "edited", func(s *store.Store, n int) (store.Note, error) {
		note, _, err := s.EditNote(n, ops[0], ops[1])
		return note, err
	})
}

// directorNoteRemove runs `guildhall director note remove <id> [--reason <text>] [--copy <dir> | --store <file>]`.
func (a *App) directorNoteRemove(args []string) int {
	var reason string
	path, _, ops, code := a.sessionOperands("director note remove", args, false, []string{"<id>"}, func(fl *flag.FlagSet) {
		fl.StringVar(&reason, "reason", "", "why the note is removed")
	})
	if code >= 0 {
		return code
	}
	return a.noteChange("director note remove", path, "removed", func(s *store.Store, n int) (store.Note, error) {
		note, _, err := s.RemoveNote(n, ops[0], reason)
		return note, err
	})
}

// noteChange makes one note change as the newest session and prints "<id> <done>".
func (a *App) noteChange(name, path, done string, change func(*store.Store, int) (store.Note, error)) int {
	note, err := withStoreClock(path, false, a.Now, func(s *store.Store) (store.Note, error) {
		d, err := s.LatestDirectorSession()
		if errors.Is(err, store.ErrNotFound) {
			return store.Note{}, errors.New("no Director session; start one with guildhall director start")
		} else if err != nil {
			return store.Note{}, err
		}
		return change(s, d.Number)
	})
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "%s %s\n", note.ID, done); err != nil {
		return 1
	}
	return 0
}

// directorNoteList runs `guildhall director note list [--copy <dir> | --store <file>]`: the live notes as
// startup.md lists them, or "No notes.".
func (a *App) directorNoteList(args []string) int {
	path, _, code := a.sessionArgs("director note list", args, false, nil)
	if code >= 0 {
		return code
	}
	lines, err := withStore(path, true, func(s *store.Store) ([]string, error) {
		lines, _, err := liveNoteLines(s, a.now())
		return lines, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director note list: %w", err))
	}
	out := "No notes.\n"
	if len(lines) > 0 {
		out = strings.Join(lines, "\n") + "\n"
	}
	if _, err := fmt.Fprint(a.Stdout, out); err != nil {
		return 1
	}
	return 0
}

// liveNoteLines returns the live notes as lines, under a "### <kind>" line for each kind that has any:
// "- n7 (s16, 2d): text", with "<repo>/<id> " after the id for a note naming a task and " [task done, check it]"
// at the end when that task is done or cancelled. It also returns the notes' total text size.
func liveNoteLines(s *store.Store, now time.Time) ([]string, int, error) {
	notes, err := s.LiveNotes()
	if err != nil {
		return nil, 0, err
	}
	var lines []string
	var kind store.NoteKind
	size := 0
	for _, n := range notes {
		size += len(n.Text)
		if n.Kind != kind {
			kind = n.Kind
			lines = append(lines, "### "+string(kind))
		}
		task, flag := "", ""
		if n.Task != "" {
			task = n.Task + " "
			repo, id, _ := strings.Cut(n.Task, "/")
			t, err := s.Task(repo, id)
			if err != nil {
				return nil, 0, err
			}
			if t.State == store.TaskDone || t.State == store.TaskCancelled {
				flag = " [task done, check it]"
			}
		}
		lines = append(lines, fmt.Sprintf("- %s %s(s%d, %s): %s%s", n.ID, task, n.Session, noteAge(now.Sub(n.ChangedAt)), n.Text, flag))
	}
	return lines, size, nil
}

// noteAge is a note's age in whole minutes under an hour, hours under a day, and days after.
func noteAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(d, 0)/time.Minute)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dd", d/(24*time.Hour))
}
