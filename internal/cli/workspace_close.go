package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/reconcile"
	"github.com/ndrwl/guildhall/internal/store"
)

// WorkspaceCloseHost is the terminal the workspace close sweep closes finished Artisans' Herdr workspaces in.
type WorkspaceCloseHost interface {
	adapter.TabHost
	adapter.IdleWorkspaceCloser
}

// paneClosed is the presentation-only marker the workspace close sweep writes in an attempt directory once the
// attempt's Herdr workspace, or its tab, is closed or found gone, so later sweeps ask Herdr nothing about it. It is
// never read as task state.
const paneClosed = "pane-closed.json"

// workspaceCloseEvery is how often a session's supervisor runs the workspace close sweep after the one it runs
// when the session starts.
const workspaceCloseEvery = 5 * time.Minute

// workspaceCloseSweep is the record-driven pass that closes the Herdr workspace of each finished Artisan attempt
// once its result is recorded and its launcher is gone (R1.54, docs/artisan.md#herdr-workspaces). It reads the
// store without writing records: it closes only what an attempt's pane-launch.json names and the live terminal
// still matches, writes paneClosed for what is closed or gone, and reports each attempt it leaves open once to
// the Director, with a message that does not wake it.
type workspaceCloseSweep struct {
	storePath, state string
	repositories     []string
	// session is the configured herdr_session and after the configured artisan_workspace_close_after; with
	// either unset or zero the sweep does nothing.
	session string
	after   time.Duration
	// open opens the terminal host for session.
	open func(ctx context.Context, session string) (WorkspaceCloseHost, error)
	// gone reports whether an attempt's owner is proven exited.
	gone func(store.Ownership) bool
	// lock takes the account-private Herdr workspace-order lock for session.
	lock   func(session string) (func(), error)
	now    func() time.Time
	stderr io.Writer
	// timeout bounds the Herdr calls for each attempt.
	timeout time.Duration
}

// workspaceCloseSweep is the sweep of session n's supervisor over the copy's store and state with cfg, cfg being
// read when the supervisor admits it.
func (a *App) workspaceCloseSweep(cfg *config.Config, storePath, state string) (*workspaceCloseSweep, error) {
	session, err := cfg.HerdrSession()
	if err != nil {
		return nil, err
	}
	after, err := cfg.ArtisanWorkspaceCloseAfter()
	if err != nil {
		return nil, err
	}
	sw := &workspaceCloseSweep{storePath: storePath, state: state, session: session, after: after,
		open: a.ArtisanWorkspaces, gone: func(o store.Ownership) bool { return reconcile.ProbeOwner(o).Gone() },
		lock: lockWorkspaceOrder, now: a.Now, stderr: a.Stderr, timeout: herdrTimeout}
	for _, r := range cfg.Repositories {
		sw.repositories = append(sw.repositories, r.ID)
	}
	if sw.open == nil {
		sw.open = openArtisanWorkspaces
	}
	return sw, nil
}

// openArtisanWorkspaces is the Herdr host, found on PATH as herdr, for the named session once its status shows the
// server running and new enough.
func openArtisanWorkspaces(ctx context.Context, session string) (WorkspaceCloseHost, error) {
	c, err := herdr.New(herdr.Options{Session: session, Command: []string{"herdr"}})
	if err != nil {
		return nil, err
	}
	if _, err := c.Status(ctx); err != nil {
		return nil, err
	}
	return adapter.NewHerdrTabs(c), nil
}

// closeCandidate is one attempt with a pane record and no paneClosed marker.
type closeCandidate struct {
	key     artisan.AttemptKey
	attempt store.StageAttempt
	record  []byte
}

// dueAttempts lists the candidates due now: finished, their launcher proven gone and their finish at least after
// old. It opens the store read-only once and reads finish times only for a repository with candidates.
func (sw *workspaceCloseSweep) dueAttempts(now time.Time) ([]closeCandidate, error) {
	return withStore(sw.storePath, true, func(s *store.Store) ([]closeCandidate, error) {
		var due []closeCandidate
		for _, repo := range sw.repositories {
			tasks, err := s.Tasks(repo)
			if err != nil {
				return nil, err
			}
			var candidates []closeCandidate
			for _, t := range tasks {
				attempts, err := s.Attempts(repo, t.ID)
				if err != nil {
					return nil, err
				}
				for _, at := range attempts {
					k := artisan.AttemptKey{Repository: repo, TaskID: at.TaskID, Step: at.Step, Attempt: at.Number}
					dir := k.Dir(sw.state)
					record, err := os.ReadFile(filepath.Join(dir, launch.PaneClaim))
					if errors.Is(err, fs.ErrNotExist) {
						continue
					} else if err != nil {
						return nil, err
					}
					if _, err := os.Lstat(filepath.Join(dir, paneClosed)); err == nil {
						continue
					} else if !errors.Is(err, fs.ErrNotExist) {
						return nil, err
					}
					candidates = append(candidates, closeCandidate{key: k, attempt: at, record: record})
				}
			}
			if len(candidates) == 0 {
				continue
			}
			finished, err := s.FinishTimes(repo)
			if err != nil {
				return nil, err
			}
			for _, c := range candidates {
				switch c.attempt.Outcome {
				case store.AttemptSucceeded, store.AttemptFailed, store.AttemptStopped:
				default:
					continue
				}
				at, ok := finished[store.AttemptName{TaskID: c.key.TaskID, Step: c.key.Step, Number: c.key.Attempt}]
				if !ok || now.Sub(at) < sw.after {
					continue
				}
				o, err := s.Ownership(repo, c.key.TaskID, c.key.Step, c.key.Attempt)
				if errors.Is(err, store.ErrNotFound) {
					continue
				} else if err != nil {
					return nil, err
				}
				if !sw.gone(o) {
					continue
				}
				due = append(due, c)
			}
		}
		return due, nil
	})
}

// run is one sweep. It builds no Herdr client without a session, with a zero delay or with nothing due; it skips
// the round while another process holds the workspace-order lock, and abandons it, with one line to stderr and
// nothing written for the attempt it was at, when Herdr cannot be asked.
func (sw *workspaceCloseSweep) run() {
	if sw.session == "" || sw.after <= 0 {
		return
	}
	now := time.Now()
	if sw.now != nil {
		now = sw.now()
	}
	due, err := sw.dueAttempts(now)
	if err != nil {
		sw.warn("read the store: %v", err)
		return
	}
	if len(due) == 0 {
		return
	}
	release, err := sw.lock(sw.session)
	if err != nil {
		return
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), sw.timeout)
	host, err := sw.open(ctx, sw.session)
	cancel()
	if err != nil {
		sw.warn("herdr session %q: %v", sw.session, err)
		return
	}
	for _, c := range due {
		ctx, cancel := context.WithTimeout(context.Background(), sw.timeout)
		reason, err := sw.close(ctx, host, c)
		cancel()
		switch {
		case err != nil:
			sw.warn("%s: %v", c.key, err)
			return
		case reason == "":
			if err := sw.mark(c.key); err != nil {
				sw.warn("%s: %v", c.key, err)
			}
		default:
			if err := sw.report(c, reason); err != nil {
				sw.warn("%s: report: %v", c.key, err)
			}
		}
	}
}

// close closes what c's pane record names when the live terminal still matches it, returning the reason it left
// it open, empty once it is closed or gone, or an error when Herdr could not be asked.
func (sw *workspaceCloseSweep) close(ctx context.Context, host WorkspaceCloseHost, c closeCandidate) (string, error) {
	var claim struct {
		State string `json:"state"`
	}
	var ref adapter.TabRef
	if json.Unmarshal(c.record, &claim) == nil && claim.State != "" {
		return fmt.Sprintf("its pane-launch.json is the %q launch claim, which names no Herdr workspace", claim.State), nil
	}
	if err := json.Unmarshal(c.record, &ref); err != nil || ref.Session == "" || ref.WorkspaceID == "" || ref.TabID == "" || ref.PaneID == "" {
		return "its pane-launch.json does not name a Herdr session, workspace, tab and pane", nil
	}
	if ref.Session != sw.session {
		return fmt.Sprintf("its Herdr workspace %s is in session %q, not the configured %q", ref.WorkspaceID, ref.Session, sw.session), nil
	}
	list, err := host.Workspaces(ctx)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(list, func(w herdr.Workspace) bool { return w.ID == ref.WorkspaceID })
	if i < 0 {
		return "", nil
	}
	tabLabel := launch.TabLabel(c.key)
	err = host.AdoptTab(ctx, ref)
	if err == nil {
		if labels := launch.AttemptWorkspaceLabels(c.key); slices.Contains(labels, list[i].Label) {
			err = host.CloseWorkspace(ctx, ref, labels, tabLabel)
		} else {
			// A release before #140 presented the attempt as a tab in a shared workspace, the Director's own among
			// them: close only that tab, never the workspace.
			err = host.CloseLabelledTab(ctx, ref, tabLabel)
		}
	}
	switch {
	case err == nil, herdr.IsNotFound(err):
		return "", nil
	case adapter.IsLeftOpen(err):
		return err.Error(), nil
	}
	return "", err
}

// mark writes the paneClosed marker in k's attempt directory.
func (sw *workspaceCloseSweep) mark(k artisan.AttemptKey) error {
	b, err := json.Marshal(struct {
		ClosedAt time.Time `json:"closed_at"`
	}{sw.clock().UTC()})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(k.Dir(sw.state), paneClosed), append(b, '\n'))
}

func (sw *workspaceCloseSweep) clock() time.Time {
	if sw.now != nil {
		return sw.now()
	}
	return time.Now()
}

// leftOpenMessage is the Director message reporting that the sweep left c's Herdr presentation open for reason. Its
// id depends only on the attempt, so sending it again is a replay, or, with another reason, refused as already sent.
func leftOpenMessage(c closeCandidate, record []byte, reason string) store.Message {
	id := fmt.Sprintf("%s.%s.%s.%s", c.key.Repository, c.key.TaskID, store.MessageHerdrLeftOpen, c.key.AttemptID())
	if len(id) > 100 {
		id = "herdr-left-open." + digest.Of([]byte(id)).Hex()
	}
	text := fmt.Sprintf("Guildhall left the Herdr presentation of attempt %s open: %s. It records %s. Inspect it and close it by hand if it is no longer needed; the attempt's records and work are unaffected.",
		c.key, reason, recordedIDs(record))
	return store.Message{ID: id, Recipient: store.RecipientDirector, Repository: c.key.Repository, TaskID: c.key.TaskID,
		Step: c.key.Step, Attempt: c.key.Attempt, Type: store.MessageHerdrLeftOpen, Text: text}
}

// recordedIDs names the Herdr ids a pane record holds, for a report.
func recordedIDs(record []byte) string {
	var ref adapter.TabRef
	if json.Unmarshal(record, &ref) != nil || ref == (adapter.TabRef{}) {
		return "no Herdr ids"
	}
	return fmt.Sprintf("session %s, workspace %s, tab %s, pane %s", strconv.Quote(ref.Session), ref.WorkspaceID, ref.TabID, ref.PaneID)
}

// report sends the left-open message for c, once: a message already sent under its id is not sent again.
func (sw *workspaceCloseSweep) report(c closeCandidate, reason string) error {
	m := leftOpenMessage(c, c.record, reason)
	_, err := withStore(sw.storePath, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.Message(m.ID); err == nil {
			return store.Receipt{}, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return store.Receipt{}, err
		}
		return s.SendMessage(m)
	})
	return err
}

func (sw *workspaceCloseSweep) warn(format string, args ...any) {
	if sw.stderr != nil {
		_, _ = fmt.Fprintf(sw.stderr, "guildhall: workspace close sweep: "+format+"\n", args...)
	}
}

// workspaceCloseWatch runs a session's workspace close sweep off the supervisor's decision path, one round at a
// time: tick starts a round when none runs and the first tick, or every since the last round started, has come;
// wait waits for a running round to end.
type workspaceCloseWatch struct {
	every time.Duration
	round func()

	mu      sync.Mutex
	last    time.Time
	started bool
	busy    bool
	running sync.WaitGroup
}

func (w *workspaceCloseWatch) tick(now time.Time) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.busy || w.started && now.Sub(w.last) < w.every {
		return
	}
	w.busy, w.started, w.last = true, true, now
	w.running.Add(1)
	go func() {
		defer w.running.Done()
		w.round()
		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
	}()
}

func (w *workspaceCloseWatch) wait() {
	if w != nil {
		w.running.Wait()
	}
}
