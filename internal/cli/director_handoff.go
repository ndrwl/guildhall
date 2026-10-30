package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// directorReload runs `guildhall director reload [--now] [--copy <dir> | --store <file> --state <dir>]`: it
// records a director-reload request, or returns the one already requested, and prints it
// (docs/director.md#reload). --now is refused when there is no session or the newest one is stopped.
func (a *App) directorReload(args []string) int {
	var now bool
	path, _, code := a.sessionArgs("director reload", args, true, func(fl *flag.FlagSet) {
		fl.BoolVar(&now, "now", false, "end the session at the end of the current turn")
	})
	if code >= 0 {
		return code
	}
	r, err := withStore(path, false, func(s *store.Store) (store.DirectorReload, error) {
		if now {
			// --now needs no note and, like a plain reload, does not wait for a running Director stage: the next
			// session restarts an open grill (D94).
			switch d, err := s.LatestDirectorSession(); {
			case errors.Is(err, store.ErrNotFound):
				return store.DirectorReload{}, errors.New("no Director session to reload; a plain guildhall director reload is applied by the next director start")
			case err != nil:
				return store.DirectorReload{}, err
			case d.State == store.DirectorStopped:
				return store.DirectorReload{}, fmt.Errorf("the Director session %d is stopped; a plain guildhall director reload is applied by the next director start", d.Number)
			}
		}
		return s.RequestDirectorReload(now)
	})
	if err != nil {
		return a.fail(fmt.Errorf("director reload: %w", err))
	}
	return a.printJSON(r)
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
		err = cp.apply(a, &storePath, nil, nil)
	}
	switch {
	case err != nil:
		return a.fail(fmt.Errorf("%s: %w", name, err))
	case fl.NArg() > 0:
		return a.fail(fmt.Errorf("%s: unexpected argument %q", name, fl.Arg(0)))
	case cp.dir == "":
		return a.fail(fmt.Errorf("%s: %w", name, cp.missing(a)))
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
