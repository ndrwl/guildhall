package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// fakeTab and fakeWorkspace are the live Herdr state a closeHost models.
type fakeTab struct {
	id, label, pane string
	panes           int
	busy            bool
}

type fakeWorkspace struct {
	id, label string
	tabs      []*fakeTab
}

// closeHost is a WorkspaceCloseHost over modelled Herdr state that applies the revalidation rules
// adapter.HerdrTabs applies (tested against Herdr's shapes in internal/adapter), and records every call.
type closeHost struct {
	mu         sync.Mutex
	session    string
	workspaces []*fakeWorkspace
	// fail fails every call, as an unreachable Herdr does.
	fail error
	// entered and release, when set, hold Workspaces until release is closed.
	entered chan struct{}
	release chan struct{}
	calls   []string
	closed  []string
}

func (h *closeHost) record(call string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, call)
	return h.fail
}

func (h *closeHost) find(tabID string) (*fakeWorkspace, *fakeTab) {
	for _, w := range h.workspaces {
		for _, t := range w.tabs {
			if t.id == tabID {
				return w, t
			}
		}
	}
	return nil, nil
}

func (h *closeHost) Workspaces(context.Context) ([]herdr.Workspace, error) {
	if h.entered != nil {
		close(h.entered)
		<-h.release
	}
	if err := h.record("workspace list"); err != nil {
		return nil, err
	}
	var out []herdr.Workspace
	for _, w := range h.workspaces {
		out = append(out, herdr.Workspace{ID: w.id, Label: w.label})
	}
	return out, nil
}

func (h *closeHost) AdoptTab(_ context.Context, ref adapter.TabRef) error {
	if err := h.record("adopt " + ref.TabID); err != nil {
		return err
	}
	if ref.Session != h.session {
		return &adapter.LeftOpenError{Reason: "another session"}
	}
	w, t := h.find(ref.TabID)
	switch {
	case t == nil:
		return &herdr.APIError{Code: "tab_not_found", Message: "tab not found"}
	case w.id != ref.WorkspaceID:
		return herdr.ErrMoved
	}
	return nil
}

func (h *closeHost) CloseWorkspace(_ context.Context, ref adapter.TabRef, labels []string, tabLabel string) error {
	if err := h.record("close workspace " + ref.WorkspaceID); err != nil {
		return err
	}
	i := slices.IndexFunc(h.workspaces, func(w *fakeWorkspace) bool { return w.id == ref.WorkspaceID })
	if i < 0 {
		return &herdr.APIError{Code: "workspace_not_found", Message: "workspace not found"}
	}
	w := h.workspaces[i]
	switch {
	case !slices.Contains(labels, w.label):
		return &adapter.LeftOpenError{Reason: "workspace labelled " + w.label}
	case len(w.tabs) != 1 || w.tabs[0].id != ref.TabID:
		return &adapter.LeftOpenError{Reason: fmt.Sprintf("workspace holds %d tabs", len(w.tabs))}
	case w.tabs[0].label != tabLabel:
		return &adapter.LeftOpenError{Reason: "tab labelled " + w.tabs[0].label}
	case w.tabs[0].panes != 1:
		return &adapter.LeftOpenError{Reason: "tab holds panes"}
	case w.tabs[0].pane != ref.PaneID:
		return herdr.ErrMoved
	case w.tabs[0].busy:
		return &adapter.LeftOpenError{Reason: "pane is not an idle shell"}
	}
	h.workspaces = slices.Delete(h.workspaces, i, i+1)
	h.closed = append(h.closed, "workspace "+ref.WorkspaceID)
	return nil
}

func (h *closeHost) CloseLabelledTab(_ context.Context, ref adapter.TabRef, label string) error {
	if err := h.record("close tab " + ref.TabID); err != nil {
		return err
	}
	w, t := h.find(ref.TabID)
	switch {
	case t == nil:
		return &herdr.APIError{Code: "tab_not_found", Message: "tab not found"}
	case t.label != label:
		return &adapter.LeftOpenError{Reason: "tab labelled " + t.label}
	case t.panes != 1:
		return &adapter.LeftOpenError{Reason: "tab holds panes"}
	case t.busy:
		return &adapter.LeftOpenError{Reason: "pane is not an idle shell"}
	}
	w.tabs = slices.DeleteFunc(w.tabs, func(x *fakeTab) bool { return x == t })
	h.closed = append(h.closed, "tab "+ref.TabID)
	return nil
}

func (h *closeHost) CreateWorkspace(context.Context, string, string) (herdr.Workspace, error) {
	return herdr.Workspace{}, errors.New("the sweep creates no workspace")
}

func (h *closeHost) CreateTab(context.Context, herdr.TabSpec) (adapter.TabRef, error) {
	return adapter.TabRef{}, errors.New("the sweep creates no tab")
}

func (h *closeHost) PaneProcessInfo(context.Context, adapter.TabRef) (herdr.ProcessInfo, error) {
	return herdr.ProcessInfo{}, errors.New("the sweep reads no pane through TabHost")
}

func (h *closeHost) RunInTab(context.Context, adapter.TabRef, string) error {
	return errors.New("the sweep types nothing")
}

func (h *closeHost) InterruptTab(context.Context, adapter.TabRef) error {
	return errors.New("the sweep interrupts nothing")
}

func (h *closeHost) callsFor(tabID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.calls {
		if strings.HasSuffix(c, " "+tabID) {
			n++
		}
	}
	return n
}

// sweepFixture is a store and state directory with attempts presented in Herdr, finished at finished, and the
// sweep over them with doubles for the terminal, the owner probe and the lock.
type sweepFixture struct {
	t                *testing.T
	storePath, state string
	finished         time.Time
	host             *closeHost
	opens            int
	notGone          map[artisan.AttemptKey]bool
	locked           bool
	stderr           bytes.Buffer
	tasks            map[string]bool
	n                int
}

const sweepSession = "guildhall-lab"

func newSweepFixture(t *testing.T) *sweepFixture {
	dir := t.TempDir()
	f := &sweepFixture{t: t, storePath: filepath.Join(dir, "guildhall.store"), state: filepath.Join(dir, "state"),
		finished: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC), host: &closeHost{session: sweepSession},
		notGone: map[artisan.AttemptKey]bool{}, tasks: map[string]bool{}}
	return f
}

func (f *sweepFixture) mutate(fn func(s *store.Store) error) {
	f.t.Helper()
	s, err := store.Open(f.storePath, store.Options{Now: func() time.Time { return f.finished }})
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := fn(s); err != nil {
		f.t.Fatal(err)
	}
}

func (f *sweepFixture) id() string {
	f.n++
	return fmt.Sprintf("m-%d", f.n)
}

// attempt records an owned attempt of step for task, finished with outcome unless it is empty, and writes record as
// its pane-launch.json; a record of nil writes none.
func (f *sweepFixture) attempt(task, step string, outcome store.AttemptOutcome, record any) artisan.AttemptKey {
	f.t.Helper()
	var k artisan.AttemptKey
	f.mutate(func(s *store.Store) error {
		if !f.tasks[task] {
			digest := "sha256:" + strings.Repeat("a", 64)
			if _, err := s.CreateTask(f.id(), store.Task{Repository: "app", ID: task, Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: digest}, ConfigDigest: digest}); err != nil {
				return err
			}
			f.tasks[task] = true
			if step == workflow.JudgeStep {
				if _, err := s.SetTaskState(f.id(), "app", task, store.TaskDone); err != nil {
					return err
				}
			}
		}
		r, err := s.StartOwnedAttempt(f.id(), store.Ownership{Repository: "app", TaskID: task, Step: step, LauncherIdentity: store.ProcessIdentity{PID: 4242, Token: "boot:4242"}})
		if err != nil {
			return err
		}
		k = artisan.AttemptKey{Repository: "app", TaskID: task, Step: step, Attempt: r.Attempt}
		if outcome != "" {
			_, err = s.FinishAttempt(f.id(), "app", task, step, r.Attempt, outcome)
		}
		return err
	})
	if record != nil {
		var b []byte
		switch r := record.(type) {
		case string:
			b = []byte(r)
		default:
			var err error
			if b, err = json.Marshal(r); err != nil {
				f.t.Fatal(err)
			}
		}
		if err := os.MkdirAll(k.Dir(f.state), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(k.Dir(f.state), launch.PaneClaim), b, 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
	return k
}

// presented adds k's per-attempt Herdr workspace, labelled label, to the live state and returns its record.
func (f *sweepFixture) presented(k artisan.AttemptKey, id, label string) adapter.TabRef {
	tab := &fakeTab{id: id + ":t2", label: launch.TabLabel(k), pane: id + ":p2", panes: 1}
	f.host.workspaces = append(f.host.workspaces, &fakeWorkspace{id: id, label: label, tabs: []*fakeTab{tab}})
	return adapter.TabRef{Session: sweepSession, WorkspaceID: id, TabID: tab.id, PaneID: tab.pane}
}

func (f *sweepFixture) workspace(id string) *fakeWorkspace {
	for _, w := range f.host.workspaces {
		if w.id == id {
			return w
		}
	}
	return nil
}

func (f *sweepFixture) sweep(after, since time.Duration) *workspaceCloseSweep {
	return &workspaceCloseSweep{storePath: f.storePath, state: f.state, repositories: []string{"app"}, session: sweepSession, after: after,
		open: func(_ context.Context, session string) (WorkspaceCloseHost, error) {
			if session != sweepSession {
				f.t.Errorf("opened session %q", session)
			}
			f.opens++
			return f.host, nil
		},
		gone: func(o store.Ownership) bool {
			return !f.notGone[artisan.AttemptKey{Repository: o.Repository, TaskID: o.TaskID, Step: o.Step, Attempt: o.Attempt}]
		},
		lock: func(session string) (func(), error) {
			if f.locked {
				return nil, errors.New("presentation lock unavailable")
			}
			return func() {}, nil
		},
		now: func() time.Time { return f.finished.Add(since) }, stderr: &f.stderr, timeout: time.Minute}
}

func (f *sweepFixture) marked(k artisan.AttemptKey) bool {
	_, err := os.Stat(filepath.Join(k.Dir(f.state), paneClosed))
	return err == nil
}

// leftOpen lists the herdr-workspace-left-open messages to the Director, unread, in send order.
func (f *sweepFixture) leftOpen() []store.Message {
	f.t.Helper()
	var out []store.Message
	_, err := withStore(f.storePath, true, func(s *store.Store) (struct{}, error) {
		page, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, 100)
		for _, m := range page {
			if m.Type == store.MessageHerdrLeftOpen {
				out = append(out, m)
			}
		}
		return struct{}{}, err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestWorkspaceCloseSweep(t *testing.T) {
	const after = 5 * time.Minute
	type result struct {
		closed  []string
		marked  bool
		report  string
		noHerdr bool
	}
	for _, tc := range []struct {
		name  string
		since time.Duration
		after time.Duration
		setup func(f *sweepFixture) artisan.AttemptKey
		want  result
	}{
		{name: "due at the delay", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
		{name: "one second before the delay", since: after - time.Second, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{noHerdr: true}},
		{name: "a delay of zero", since: time.Hour, after: -1, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{noHerdr: true}},
		{name: "an unfinished attempt", since: time.Hour, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", "", nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{noHerdr: true}},
		{name: "an owner not proven gone", since: time.Hour, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			f.notGone[k] = true
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{noHerdr: true}},
		{name: "a success intake rejected", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			f.mutate(func(s *store.Store) error {
				_, err := s.RejectAttempt(f.id(), "app", "t-1", "build", 1, "no code head")
				return err
			})
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
		{name: "a failed attempt", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptFailed, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
		{name: "a stopped attempt", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptStopped, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
		{name: "a stopped attempt before the delay", since: after - time.Second, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptStopped, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{noHerdr: true}},
		{name: "a launching claim", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptFailed, `{"state":"launching"}`+"\n")
			f.presented(k, "w2", launch.WorkspaceLabel(k))
			return k
		}, want: result{report: "launch claim"}},
		{name: "an unparsable record", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			return f.attempt("t-1", "build", store.AttemptFailed, "{")
		}, want: result{report: "does not name"}},
		{name: "a workspace already gone", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			return f.writeRecord(k, adapter.TabRef{Session: sweepSession, WorkspaceID: "w7", TabID: "w7:t2", PaneID: "w7:p2"})
		}, want: result{marked: true}},
		{name: "a tab already gone", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			f.workspace("w2").tabs[0].id = "w2:t9"
			return f.writeRecord(k, ref)
		}, want: result{marked: true}},
		{name: "a recorded id now labelled otherwise", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", "notes")
			f.workspace("w2").tabs[0].label = "shell"
			return f.writeRecord(k, ref)
		}, want: result{report: "tab labelled shell"}},
		{name: "a per-attempt workspace whose tab is relabelled", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			f.workspace("w2").tabs[0].label = "shell"
			return f.writeRecord(k, ref)
		}, want: result{report: "tab labelled shell"}},
		{name: "an extra tab", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			w := f.workspace("w2")
			w.tabs = append(w.tabs, &fakeTab{id: "w2:t3", label: "mine", pane: "w2:p3", panes: 1})
			return f.writeRecord(k, ref)
		}, want: result{report: "holds 2 tabs"}},
		{name: "an extra pane", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			f.workspace("w2").tabs[0].panes = 2
			return f.writeRecord(k, ref)
		}, want: result{report: "holds panes"}},
		{name: "a busy shell", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			f.workspace("w2").tabs[0].busy = true
			return f.writeRecord(k, ref)
		}, want: result{report: "not an idle shell"}},
		{name: "a moved pane", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			f.workspace("w2").tabs[0].pane = "w2:p9"
			return f.writeRecord(k, ref)
		}, want: result{report: "moved"}},
		{name: "a record from another session", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			ref := f.presented(k, "w2", launch.WorkspaceLabel(k))
			ref.Session = "other-lab"
			return f.writeRecord(k, ref)
		}, want: result{report: `session "other-lab"`}},
		{name: "an old-layout tab in the Director's workspace", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			f.host.workspaces = append(f.host.workspaces, &fakeWorkspace{id: "w1", label: "director", tabs: []*fakeTab{
				{id: "w1:t1", label: "director", pane: "w1:p1", panes: 1, busy: true},
				{id: "w1:t3", label: launch.TabLabel(k), pane: "w1:p3", panes: 1},
			}})
			return f.writeRecord(k, adapter.TabRef{Session: sweepSession, WorkspaceID: "w1", TabID: "w1:t3", PaneID: "w1:p3"})
		}, want: result{closed: []string{"tab w1:t3"}, marked: true}},
		{name: "a #140 workspace labelled as its tab", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.TabLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
		{name: "a judge attempt", since: after, setup: func(f *sweepFixture) artisan.AttemptKey {
			k := f.attempt("t-1", workflow.JudgeStep, store.AttemptSucceeded, nil)
			return f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
		}, want: result{closed: []string{"workspace w2"}, marked: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSweepFixture(t)
			k := tc.setup(f)
			delay := after
			if tc.after < 0 {
				delay = 0
			} else if tc.after > 0 {
				delay = tc.after
			}
			before := len(f.host.workspaces)
			f.sweep(delay, tc.since).run()
			if tc.want.noHerdr && (f.opens != 0 || len(f.host.calls) != 0) {
				t.Errorf("Herdr opened %d times and called %q, want no Herdr", f.opens, f.host.calls)
			}
			if !slices.Equal(f.host.closed, tc.want.closed) {
				t.Errorf("closed %q, want %q", f.host.closed, tc.want.closed)
			}
			if got := f.marked(k); got != tc.want.marked {
				t.Errorf("marked = %v, want %v", got, tc.want.marked)
			}
			reports := f.leftOpen()
			switch {
			case tc.want.report == "" && len(reports) != 0:
				t.Errorf("reported %+v, want none", reports)
			case tc.want.report != "" && len(reports) != 1:
				t.Fatalf("reported %+v, want one report", reports)
			case tc.want.report != "":
				m := reports[0]
				if want := "app.t-1." + store.MessageHerdrLeftOpen + "." + k.AttemptID(); m.ID != want || m.Step != k.Step || m.Attempt != k.Attempt {
					t.Errorf("report %+v, want id %s for %s", m, want, k)
				}
				if m.Acknowledged {
					t.Errorf("report %s is acknowledged, want it queued unread", m.ID)
				}
				if !strings.Contains(m.Text, tc.want.report) || !strings.Contains(m.Text, k.String()) {
					t.Errorf("report text %q, want it to name %s and %q", m.Text, k, tc.want.report)
				}
				if len(f.host.workspaces) != before {
					t.Errorf("a left-open attempt closed a workspace")
				}
			}
			if w := f.workspace("w1"); w != nil && !slices.ContainsFunc(w.tabs, func(t *fakeTab) bool { return t.id == "w1:t1" }) {
				t.Errorf("the Director's tab was closed: %+v", w.tabs)
			}
			if tc.want.report == "" && f.stderr.Len() != 0 {
				t.Errorf("stderr %q", f.stderr.String())
			}
		})
	}
}

// directorConfig is the run tests' config with local added to its [local] table.
func directorConfig(t *testing.T, local string) *config.Config {
	t.Helper()
	data := strings.Replace(directorConfigWith(t, ""), "[local]\n", "[local]\n"+local, 1)
	cfg, err := config.Parse([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// writeRecord writes ref as k's pane-launch.json and returns k.
func (f *sweepFixture) writeRecord(k artisan.AttemptKey, ref adapter.TabRef) artisan.AttemptKey {
	f.t.Helper()
	b, err := json.Marshal(ref)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(k.Dir(f.state), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k.Dir(f.state), launch.PaneClaim), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return k
}

func TestWorkspaceCloseSweepWithoutASessionCallsNoHerdr(t *testing.T) {
	f := newSweepFixture(t)
	k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
	f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
	sw := f.sweep(time.Minute, time.Hour)
	sw.session = ""
	sw.run()
	if f.opens != 0 || len(f.host.calls) != 0 || f.marked(k) {
		t.Errorf("without herdr_session: opens %d, calls %q, marked %v", f.opens, f.host.calls, f.marked(k))
	}
	cfg := directorConfig(t, "")
	a, _, _ := newTestApp(nil, nil)
	a.ArtisanWorkspaces = func(context.Context, string) (WorkspaceCloseHost, error) {
		t.Error("a sweep without herdr_session opened Herdr")
		return f.host, nil
	}
	built, err := a.workspaceCloseSweep(cfg, f.storePath, f.state)
	if err != nil {
		t.Fatal(err)
	}
	built.now = func() time.Time { return f.finished.Add(time.Hour) }
	built.gone = func(store.Ownership) bool { return true }
	built.run()
	if f.marked(k) {
		t.Error("a sweep without herdr_session marked the attempt")
	}
}

func TestWorkspaceCloseSweepAbandonsTheRoundWhenHerdrFails(t *testing.T) {
	f := newSweepFixture(t)
	k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
	f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
	l := f.attempt("t-2", "build", store.AttemptSucceeded, `{"state":"launching"}`)
	f.host.fail = errors.New("herdr stopped: context deadline exceeded")
	before, err := withStore(f.storePath, true, func(s *store.Store) ([]store.Receipt, error) { return s.Receipts() })
	if err != nil {
		t.Fatal(err)
	}
	f.sweep(time.Minute, time.Hour).run()
	after, err := withStore(f.storePath, true, func(s *store.Store) ([]store.Receipt, error) { return s.Receipts() })
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Errorf("a failed round wrote %d store mutations", len(after)-len(before))
	}
	if f.marked(k) || f.marked(l) {
		t.Error("a failed round wrote a marker")
	}
	if got := strings.Count(f.stderr.String(), "\n"); got != 1 || !strings.Contains(f.stderr.String(), "deadline exceeded") {
		t.Errorf("stderr %q, want one line naming the error", f.stderr.String())
	}
	f.host.fail = nil
	f.stderr.Reset()
	f.sweep(time.Minute, time.Hour).run()
	if !f.marked(k) || len(f.leftOpen()) != 1 {
		t.Errorf("the next round: marked %v, reports %+v", f.marked(k), f.leftOpen())
	}
}

func TestWorkspaceCloseSweepSkipsTheRoundWhileOrderingHoldsTheLock(t *testing.T) {
	f := newSweepFixture(t)
	k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
	f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
	f.locked = true
	f.sweep(time.Minute, time.Hour).run()
	if f.opens != 0 || len(f.host.calls) != 0 || f.marked(k) {
		t.Errorf("with the lock held: opens %d, calls %q, marked %v", f.opens, f.host.calls, f.marked(k))
	}
}

func TestWorkspaceCloseSweepReportsOnceAndForgetsWhatItClosed(t *testing.T) {
	f := newSweepFixture(t)
	closed := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
	f.writeRecord(closed, f.presented(closed, "w2", launch.WorkspaceLabel(closed)))
	busy := f.attempt("t-2", "build", store.AttemptSucceeded, nil)
	f.writeRecord(busy, f.presented(busy, "w3", launch.WorkspaceLabel(busy)))
	f.workspace("w3").tabs[0].busy = true
	f.sweep(time.Minute, time.Hour).run()
	if !f.marked(closed) || f.marked(busy) {
		t.Fatalf("first sweep: marked %v and %v", f.marked(closed), f.marked(busy))
	}
	first := f.leftOpen()
	if len(first) != 1 {
		t.Fatalf("first sweep reported %+v", first)
	}
	f.host.calls = nil
	f.sweep(time.Minute, time.Hour+workspaceCloseEvery).run()
	if n := f.host.callsFor("w2:t2") + f.host.callsFor("w2"); n != 0 {
		t.Errorf("the second sweep asked Herdr about a marked attempt: %q", f.host.calls)
	}
	if got := f.leftOpen(); len(got) != 1 || got[0].ID != first[0].ID || got[0].Sequence != first[0].Sequence {
		t.Errorf("the second sweep reported %+v, want only the first report", got)
	}
	// Another reason later is not reported again; a later sweep closes the workspace once it is safe.
	f.workspace("w3").tabs[0].busy = false
	f.workspace("w3").tabs[0].panes = 2
	f.sweep(time.Minute, 2*time.Hour).run()
	if got := f.leftOpen(); len(got) != 1 || f.stderr.Len() != 0 {
		t.Errorf("a changed reason reported %+v, stderr %q", got, f.stderr.String())
	}
	f.workspace("w3").tabs[0].panes = 1
	f.sweep(time.Minute, 3*time.Hour).run()
	if !f.marked(busy) || !slices.Equal(f.host.closed, []string{"workspace w2", "workspace w3"}) {
		t.Errorf("once safe: marked %v, closed %q", f.marked(busy), f.host.closed)
	}
}

func TestWorkspaceCloseSweepAfterARestartClosesWhatBecameDue(t *testing.T) {
	f := newSweepFixture(t)
	k := f.attempt("t-1", "build", store.AttemptSucceeded, nil)
	f.writeRecord(k, f.presented(k, "w2", launch.WorkspaceLabel(k)))
	// The supervisor's last sweep ran before the attempt was due, then the supervisor ended.
	f.sweep(5*time.Minute, time.Minute).run()
	if f.marked(k) || len(f.host.closed) != 0 {
		t.Fatal("closed before due")
	}
	// A new supervisor, started an hour later, sweeps at its start.
	a, _, stderr := newTestApp(nil, nil)
	a.Now = func() time.Time { return f.finished.Add(time.Hour) }
	a.ArtisanWorkspaces = func(context.Context, string) (WorkspaceCloseHost, error) { return f.host, nil }
	cfg := directorConfig(t, "herdr_session = \""+sweepSession+"\"\n")
	sw, err := a.workspaceCloseSweep(cfg, f.storePath, f.state)
	if err != nil {
		t.Fatal(err)
	}
	if sw.after != 5*time.Minute || sw.session != sweepSession || !slices.Equal(sw.repositories, []string{"app"}) {
		t.Fatalf("sweep from config = %+v", sw)
	}
	sw.gone, sw.lock = func(store.Ownership) bool { return true }, func(string) (func(), error) { return func() {}, nil }
	w := &workspaceCloseWatch{every: workspaceCloseEvery, round: sw.run}
	w.tick(time.Now())
	w.wait()
	if !f.marked(k) || !slices.Equal(f.host.closed, []string{"workspace w2"}) {
		t.Errorf("after a restart: marked %v, closed %q, stderr %q", f.marked(k), f.host.closed, stderr)
	}
}

func TestWorkspaceCloseWatchGatesRoundsAtFiveMinutes(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	var rounds int
	w := &workspaceCloseWatch{every: workspaceCloseEvery, round: func() { rounds++ }}
	w.tick(at)
	w.wait()
	if rounds != 1 {
		t.Fatalf("the first tick ran %d rounds, want the start sweep", rounds)
	}
	w.tick(at.Add(workspaceCloseEvery - time.Second))
	w.wait()
	if rounds != 1 {
		t.Fatalf("a tick before 5 minutes ran a round")
	}
	w.tick(at.Add(workspaceCloseEvery))
	w.wait()
	if rounds != 2 {
		t.Fatalf("a tick at 5 minutes ran %d rounds, want 2", rounds)
	}
	if workspaceCloseEvery != 5*time.Minute {
		t.Errorf("workspaceCloseEvery = %v, want 5m", workspaceCloseEvery)
	}
}

func TestWorkspaceCloseWatchRunsOneRoundAtATimeOffTheTick(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	w := &workspaceCloseWatch{every: time.Nanosecond, round: func() { entered <- struct{}{}; <-release }}
	done := make(chan struct{})
	go func() {
		w.tick(at)
		w.tick(at.Add(time.Hour))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a blocked round delayed the tick")
	}
	<-entered
	close(release)
	w.wait()
	if len(entered) != 0 {
		t.Errorf("a tick during a running round started another")
	}
}
