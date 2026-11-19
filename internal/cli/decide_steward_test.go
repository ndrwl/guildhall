package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// stewardDecisionStore is a store file named name in a new directory, holding the Steward's reserved tasks and an
// open decision "approval-1" on each, with a running fake-runtime session bound to a pane when session is set.
func stewardDecisionStore(t *testing.T, name string, session bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ensureStewardRecords(path); err != nil {
		t.Fatal(err)
	}
	if _, err := withStore(path, false, func(s *store.Store) (struct{}, error) {
		for _, task := range []string{stewardRolloutsTask, stewardSetupTask, stewardSecretsTask, stewardIncidentsTask, stewardLessonsTask, stewardGuildmasterTk} {
			d := store.Decision{Repository: stewardRepository, TaskID: task, ID: "approval-1", Kind: store.DecisionQuestion, Question: "Approve plan 1?",
				Options: []string{stewardApprove, stewardDecline}}
			if _, err := s.OpenDecision("test-open:"+task, d); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if session {
		startSession(t, path, store.DirectorPane{Session: "lab", Workspace: "w1", Tab: "w1:t2", Pane: "w1:p2"})
	}
	return path
}

// stewardAnswer runs decide answer for decision approval-1 of task in the store at path, outside any session, with
// the Guildmaster's words carrying a phrase no message may quote.
func stewardAnswer(t *testing.T, path, task string, panes *typedPanes) (int, string, string) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	app.Stdin = strings.NewReader("approve\nThe secret phrase is plum.\n")
	app.DirectorPane = panes
	code := app.Run([]string{"decide", "answer", "--store", path, "--repo", stewardRepository, "--task", task, "--decision", "approval-1", "--answer-file", "-"})
	return code, stdout.String(), stderr.String()
}

// stewardInbox is the Steward's inbox and every recorded wake of the store at path.
func stewardInbox(t *testing.T, path string) ([]store.Message, []store.Wake) {
	t.Helper()
	ms, err := withStore(path, true, func(s *store.Store) ([]store.Message, error) {
		ms, _, err := s.Inbox(store.Recipient{Role: store.RecipientSteward}, 0, store.MaxInboxLimit)
		return ms, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ms, readWakes(t, path)
}

// TestDecideAnswerWakesTheSteward (red-first, H125): answering a steward/setup decision outside a session tells the
// Steward with one decision-answered message from the Guildmaster, naming the decision but not the answer, and wakes
// its session once; answering again replays both and types nothing more.
func TestDecideAnswerWakesTheSteward(t *testing.T) {
	path := stewardDecisionStore(t, stewardStoreFile, true)
	panes := &typedPanes{}
	code, out, stderr := stewardAnswer(t, path, stewardSetupTask, panes)
	if code != 0 || stderr != "" {
		t.Fatalf("decide answer: exit %d, stderr %s", code, stderr)
	}
	if r := decodeJSON[store.Receipt](t, out); r.MutationID == "" {
		t.Fatalf("receipt = %s", out)
	}
	ms, ws := stewardInbox(t, path)
	if len(ms) != 1 {
		t.Fatalf("Steward inbox = %+v, want one message", ms)
	}
	m := ms[0]
	if m.Type != store.MessageDecisionAnswered || m.Sender != store.SenderGuildmaster || m.Recipient != store.RecipientSteward ||
		m.Repository != stewardRepository || m.TaskID != stewardSetupTask || m.Acknowledged ||
		!strings.Contains(m.Text, "approval-1") || strings.Contains(m.Text, "plum") {
		t.Errorf("message = %+v, want an unread decision-answered from the Guildmaster naming approval-1 and not the answer", m)
	}
	w, err := withStore(path, true, func(s *store.Store) (store.Wake, error) { return s.WakeFor(m.ID) })
	if err != nil || w.Decision != store.WakeWoke || w.SenderType != store.SenderGuildmaster {
		t.Errorf("wake for %s = %+v, %v; want woke by the Guildmaster", m.ID, w, err)
	}
	if len(ws) != 1 || len(panes.lines) != 1 || !strings.HasPrefix(panes.lines[0], "w1:p2 ") {
		t.Fatalf("wakes %+v, typed %q; want one of each", ws, panes.lines)
	}

	code, again, stderr := stewardAnswer(t, path, stewardSetupTask, panes)
	if code != 0 || stderr != "" || again != out {
		t.Fatalf("answer again: exit %d, %s, stderr %s; want the receipt %s", code, again, stderr, out)
	}
	if ms, ws := stewardInbox(t, path); len(ms) != 1 || len(ws) != 1 || len(panes.lines) != 1 {
		t.Errorf("answer again: inbox %+v, wakes %+v, typed %q; want nothing new", ms, ws, panes.lines)
	}
}

// TestDecideAnswerStewardNoSession: with no Steward session running, the answer still exits 0 and the message waits
// unread with a queued wake.
func TestDecideAnswerStewardNoSession(t *testing.T) {
	path := stewardDecisionStore(t, stewardStoreFile, false)
	panes := &typedPanes{}
	if code, _, stderr := stewardAnswer(t, path, stewardRolloutsTask, panes); code != 0 || stderr != "" {
		t.Fatalf("decide answer: exit %d, stderr %s", code, stderr)
	}
	ms, ws := stewardInbox(t, path)
	if len(ms) != 1 || ms[0].Acknowledged || len(ws) != 1 || ws[0].MessageID != ms[0].ID || ws[0].Decision != store.WakeQueued || len(panes.lines) != 0 {
		t.Errorf("inbox %+v, wakes %+v, typed %q; want one unread message with a queued wake", ms, ws, panes.lines)
	}
}

// TestDecideAnswerStewardEachReservedTask: each of the Steward's reserved tasks sends one message for its answer, and
// a Steward task that is not reserved sends none.
func TestDecideAnswerStewardEachReservedTask(t *testing.T) {
	path := stewardDecisionStore(t, stewardStoreFile, true)
	panes := &typedPanes{}
	reserved := []string{stewardRolloutsTask, stewardSetupTask, stewardSecretsTask, stewardIncidentsTask, stewardLessonsTask}
	for i, task := range reserved {
		if code, _, stderr := stewardAnswer(t, path, task, panes); code != 0 || stderr != "" {
			t.Fatalf("%s: exit %d, stderr %s", task, code, stderr)
		}
		ms, _ := stewardInbox(t, path)
		if len(ms) != i+1 || ms[i].TaskID != task || ms[i].Type != store.MessageDecisionAnswered {
			t.Fatalf("%s: inbox %+v, want one more decision-answered for it", task, ms)
		}
	}
	if code, _, stderr := stewardAnswer(t, path, stewardGuildmasterTk, panes); code != 0 || stderr != "" {
		t.Fatalf("%s: exit %d, stderr %s", stewardGuildmasterTk, code, stderr)
	}
	if ms, ws := stewardInbox(t, path); len(ms) != len(reserved) || len(ws) != len(reserved) {
		t.Errorf("a task that is not reserved: inbox %+v, wakes %+v; want nothing new", ms, ws)
	}
}

// TestDecideAnswerDirectorStoreWakesNothing (pin): the same answer in a Director store, with a session and a pane,
// sends no message, records no wake and types nothing.
func TestDecideAnswerDirectorStoreWakesNothing(t *testing.T) {
	path := stewardDecisionStore(t, "director.store", true)
	panes := &typedPanes{}
	if code, _, stderr := stewardAnswer(t, path, stewardSetupTask, panes); code != 0 || stderr != "" {
		t.Fatalf("decide answer: exit %d, stderr %s", code, stderr)
	}
	ms, ws := stewardInbox(t, path)
	director, err := withStore(path, true, func(s *store.Store) ([]store.Message, error) {
		ms, _, err := s.Inbox(store.Recipient{Role: store.RecipientDirector}, 0, store.MaxInboxLimit)
		return ms, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 0 || len(director) != 0 || len(ws) != 0 || len(panes.lines) != 0 {
		t.Errorf("Director store: Steward inbox %+v, Director inbox %+v, wakes %+v, typed %q; want none", ms, director, ws, panes.lines)
	}
}
