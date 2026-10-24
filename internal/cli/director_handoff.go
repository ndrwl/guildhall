package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// directorHandoff runs `guildhall director handoff --note-file <path|-> [--copy <dir> | --store <file> --state
// <dir>]`: the note, read with the decision-answer encoding rules and at most store.MaxHandoffNoteBytes, is
// written to <state>/documents/director/handoff-<seq>.md and recorded as the newest session's handoff note,
// which is printed (docs/director.md#handoff).
func (a *App) directorHandoff(args []string) int {
	var noteFile string
	path, state, code := a.sessionArgs("director handoff", args, true, func(fl *flag.FlagSet) {
		fl.StringVar(&noteFile, "note-file", "", "the note, or - for standard input")
	})
	if code >= 0 {
		return code
	}
	if noteFile == "" {
		return a.usageError("director handoff: --note-file is required: a file, or - for standard input")
	}
	text, err := a.readText("note", noteFile, store.MaxHandoffNoteBytes)
	switch {
	case err != nil:
		return a.fail(fmt.Errorf("director handoff: %w", err))
	case strings.TrimSpace(text) == "":
		return a.fail(errors.New("director handoff: the note is empty"))
	case len(text) > store.MaxHandoffNoteBytes:
		return a.fail(fmt.Errorf("director handoff: the note is %d bytes; the limit is %d", len(text), store.MaxHandoffNoteBytes))
	}
	sum := sha256.Sum256([]byte(text))
	docs := filepath.Join(state, "documents")
	note, err := withStore(path, false, func(s *store.Store) (store.HandoffNote, error) {
		d, err := s.LatestDirectorSession()
		if errors.Is(err, store.ErrNotFound) {
			return store.HandoffNote{}, errors.New("no Director session; start one with guildhall director start")
		} else if err != nil {
			return store.HandoffNote{}, err
		}
		note, _, err := s.RecordHandoffNote(d.Number, hex.EncodeToString(sum[:]), func(p string) error {
			file := filepath.Join(docs, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				return err
			}
			return atomicfile.Write(file, []byte(text))
		})
		return note, err
	})
	if err != nil {
		return a.fail(fmt.Errorf("director handoff: %w", err))
	}
	return a.printJSON(note)
}

// directorReload runs `guildhall director reload [--now] [--copy <dir> | --store <file> --state <dir>]`: it
// records a director-reload request, or returns the one already requested, and prints it
// (docs/director.md#reload). --now is refused unless the newest handoff note was recorded by the newest session,
// which is not stopped, after every running Director stage attempt started.
func (a *App) directorReload(args []string) int {
	var now bool
	path, state, code := a.sessionArgs("director reload", args, true, func(fl *flag.FlagSet) {
		fl.BoolVar(&now, "now", false, "end the session at the end of the current turn")
	})
	if code >= 0 {
		return code
	}
	r, err := withStore(path, false, func(s *store.Store) (store.DirectorReload, error) {
		if now {
			if err := admitReloadNow(s, state); err != nil {
				return store.DirectorReload{}, err
			}
		}
		return s.RequestDirectorReload(now)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director reload: %w", err))
	}
	return a.printJSON(r)
}

// admitReloadNow refuses reload --now unless the newest session is live and recorded the newest handoff note,
// and that note was recorded after every running Director stage attempt started.
func admitReloadNow(s *store.Store, state string) error {
	d, err := s.LatestDirectorSession()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("no Director session to reload; a plain guildhall director reload is applied by the next director start")
	case err != nil:
		return err
	case d.State == store.DirectorStopped:
		return fmt.Errorf("the Director session %d is stopped; a plain guildhall director reload is applied by the next director start", d.Number)
	}
	note, err := s.LatestHandoffNote()
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("no handoff note; record one with guildhall director handoff --note-file <file> first")
	case err != nil:
		return err
	case note.Session != d.Number:
		return fmt.Errorf("the newest handoff note was recorded by session %d, not the current session %d; record one with guildhall director handoff first", note.Session, d.Number)
	}
	starts, err := runningDirectorStages(s, state)
	if err != nil {
		return err
	}
	for k, started := range starts {
		if started > note.Sequence {
			return fmt.Errorf("the Director stage %s started after the newest handoff note; record a note that carries its open questions with guildhall director handoff first", k)
		}
	}
	return nil
}

// runningDirectorStages returns each running attempt the engine handed to the Director as a Director stage, with
// the sequence of the receipt that started it, read from the store and the attempt directories under state.
func runningDirectorStages(s *store.Store, state string) (map[artisan.AttemptKey]uint64, error) {
	v, err := s.DirectorView()
	if err != nil {
		return nil, err
	}
	out := map[artisan.AttemptKey]uint64{}
	for _, at := range v.Attempts {
		if at.Outcome != "" {
			continue
		}
		k := artisan.AttemptKey{Repository: at.Repository, TaskID: at.TaskID, Step: at.Step, Attempt: at.Number}
		handed, err := interp.HandedToDirector(k.Dir(state))
		if err != nil {
			return nil, err
		}
		if !handed {
			continue
		}
		record := fmt.Sprintf("attempt/%s/%d", at.Step, at.Number)
		for _, r := range v.Receipts {
			if r.Repository == at.Repository && r.TaskID == at.TaskID && r.Record == record {
				out[k] = r.Sequence
				break
			}
		}
	}
	return out, nil
}

// turnSignalTimeout bounds one turn signal, so a hook never holds the runtime long.
const turnSignalTimeout = 30 * time.Second

// directorTurnSignal runs `guildhall director turn-started|turn-ended [--copy <dir>]`, the runtime's turn-start
// and end-of-turn hooks: it signals turn to the newest session's supervisor over its session endpoint
// (docs/director.md#the-end-of-a-turn). It never exits 2, which a Claude Code Stop hook reads as "keep going"
// and a UserPromptSubmit hook as "block the prompt": a usage error is exit 1, as is no supervisor listening.
func (a *App) directorTurnSignal(turn session.Turn, args []string) int {
	name := "director " + string(turn)
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	var storePath string
	err := fl.Parse(args)
	if err == nil {
		err = cp.apply(a.Getenv, &storePath, nil, nil)
	}
	switch {
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case fl.NArg() > 0:
		return a.fail(fmt.Errorf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return a.fail(fmt.Errorf("%s: --copy is required, or GUILDHALL_COPY", name))
	}
	copyDir, err := filepath.Abs(cp.dir)
	if err != nil {
		return a.fail(err)
	}
	if _, err := os.Stat(storePath); err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, noStore(storePath)))
	}
	d, err := withStore(storePath, true, func(s *store.Store) (store.DirectorSession, error) { return s.LatestDirectorSession() })
	if errors.Is(err, store.ErrNotFound) {
		return a.fail(fmt.Errorf("%s: no Director session", name))
	} else if err != nil {
		return a.fail(fmt.Errorf("%s: %w", name, err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnSignalTimeout)
	defer cancel()
	dir := filepath.Join(copyDir, "director", fmt.Sprintf("session-%d", d.Number))
	if turn == session.TurnEnded {
		if err := a.contextFromTurn(dir); err != nil {
			_, _ = fmt.Fprintf(a.Stderr, "Lanterns: context refresh unavailable: %v\n", err)
		}
	}
	if err := session.Signal(ctx, dir, d.Number, turn); errors.Is(err, session.ErrPath) {
		// The supervisor could not bind there either, so it waits out the grace instead.
		return a.fail(fmt.Errorf("%s: session %d: no supervisor endpoint can listen: %w", name, d.Number, err))
	} else if err != nil {
		return a.fail(fmt.Errorf("%s: session %d: %w", name, d.Number, err))
	}
	return 0
}
