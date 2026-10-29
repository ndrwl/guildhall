package messaging_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/session"
	"github.com/ndrwl/guildhall/internal/store"
)

// handoffHarness is a real store holding task app/t-1 and session 1 of the fake runtime, which wakes are typed
// into, running in directorPane under policy.
func handoffHarness(t *testing.T, policy store.HandoffPolicy) *storeHarness {
	t.Helper()
	return handoffHarnessFor(t, policy, store.TypedWakeAdapter)
}

// handoffHarnessFor is handoffHarness with the session running the runtime adapter.
func handoffHarnessFor(t *testing.T, policy store.HandoffPolicy, adapter string) *storeHarness {
	t.Helper()
	h := newStoreHarness(t)
	h.CreateTask(t, "app", "t-1")
	d := store.DirectorSession{Number: 1, State: store.DirectorRecovering, Identity: store.ProcessIdentity{PID: 7, Token: "boot:7"}, Release: "dev",
		ConfigDigest: "sha256:" + strings.Repeat("c", 64), Pane: directorPane, Handoff: policy, Adapter: adapter, Dir: "/copy/director/session-1"}
	r, err := h.s.StartDirectorSession(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.s.SetDirectorState(1, store.DirectorRunning, r.Sequence); err != nil {
		t.Fatal(err)
	}
	return h
}

// finishTask creates task app/<id> and moves it to done.
func finishTask(t *testing.T, h *storeHarness, id string) {
	t.Helper()
	h.CreateTask(t, "app", id)
	if _, err := h.s.SetTaskState("done-"+id, "app", id, store.TaskDone); err != nil {
		t.Fatal(err)
	}
}

// wakeFor sends message id to the Director and wakes it, returning the line typed, if any.
func wakeFor(t *testing.T, h *storeHarness, pane *recordingPane, id string) string {
	t.Helper()
	before := len(pane.typed)
	sendDirector(t, h, id, "note", "Carry on.")
	mustWake(t, h, pane, id, messaging.SenderGuildhall)
	if len(pane.typed) == before {
		return ""
	}
	return strings.TrimPrefix(pane.typed[len(pane.typed)-1], directorPane.Pane+" ")
}

func reportContext(t *testing.T, file string, percent int) {
	t.Helper()
	if err := session.WriteContext(filepath.Dir(file), session.Context{UsedPercent: percent, ReportedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func dues(t *testing.T, h *storeHarness) []store.HandoffDue {
	t.Helper()
	got, err := h.s.HandoffDues(1)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestWakeCarriesTheTaskTriggerOncePerCrossing: every handoff_after_tasks finished tasks is one crossing, carried
// by the next wake line only and recorded with that wake's message.
func TestWakeCarriesTheTaskTriggerOncePerCrossing(t *testing.T) {
	h := handoffHarness(t, store.HandoffPolicy{AfterTasks: 2})
	pane := &recordingPane{}
	finishTask(t, h, "t-2")
	if got := wakeFor(t, h, pane, "n-1"); got != "guildhall: 1 unread (note 1 from guildmaster)" {
		t.Fatalf("one task finished: typed %q", got)
	}
	finishTask(t, h, "t-3")
	if got := wakeFor(t, h, pane, "n-2"); got != "guildhall: 2 unread (note 2 from guildmaster); handoff due (2 tasks finished)" {
		t.Fatalf("two tasks finished: typed %q", got)
	}
	if got := wakeFor(t, h, pane, "n-3"); got != "guildhall: 3 unread (note 3 from guildmaster)" {
		t.Fatalf("the crossing again: typed %q", got)
	}
	finishTask(t, h, "t-4")
	finishTask(t, h, "t-5")
	if got := wakeFor(t, h, pane, "n-4"); got != "guildhall: 4 unread (note 4 from guildmaster); handoff due (4 tasks finished)" {
		t.Fatalf("four tasks finished: typed %q", got)
	}
	got := dues(t, h)
	if len(got) != 2 || got[0].Trigger != store.HandoffTasks || got[0].Crossing != 1 || got[0].MessageID != "n-2" ||
		got[1].Crossing != 2 || got[1].Reason != "4 tasks finished" || got[1].MessageID != "n-4" {
		t.Fatalf("dues = %+v", got)
	}
}

// TestWakeCarriesTheContextTriggerOncePerSession: the context trigger crosses once the report reaches the
// threshold and not again in the session; no report, or an unreadable one, is not a crossing and no error.
func TestWakeCarriesTheContextTriggerOncePerSession(t *testing.T) {
	file := filepath.Join(t.TempDir(), session.ContextFile)
	h := handoffHarness(t, store.HandoffPolicy{ContextPercent: 70, ContextFile: file})
	pane := &recordingPane{}
	if got := wakeFor(t, h, pane, "n-1"); got != "guildhall: 1 unread (note 1 from guildmaster)" {
		t.Fatalf("no report: typed %q", got)
	}
	reportContext(t, file, 69)
	if got := wakeFor(t, h, pane, "n-2"); strings.Contains(got, "handoff") {
		t.Fatalf("below the threshold: typed %q", got)
	}
	reportContext(t, file, 72)
	if got := wakeFor(t, h, pane, "n-3"); got != "guildhall: 3 unread (note 3 from guildmaster); handoff due (context 72%)" {
		t.Fatalf("at 72%%: typed %q", got)
	}
	reportContext(t, file, 90)
	if got := wakeFor(t, h, pane, "n-4"); strings.Contains(got, "handoff") {
		t.Fatalf("crossed already: typed %q", got)
	}
	if err := os.WriteFile(file, []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := wakeFor(t, h, pane, "n-5"); got != "guildhall: 5 unread (note 5 from guildmaster)" {
		t.Fatalf("unreadable report: typed %q", got)
	}
	if got := dues(t, h); len(got) != 1 || got[0].Trigger != store.HandoffContext || got[0].Reason != "context 72%" || got[0].MessageID != "n-3" {
		t.Fatalf("dues = %+v", got)
	}
}

// TestWakeJoinsTriggersThatHoldTogether: two triggers crossing at once share one suffix, context first, and each
// is recorded.
func TestWakeJoinsTriggersThatHoldTogether(t *testing.T) {
	file := filepath.Join(t.TempDir(), session.ContextFile)
	h := handoffHarness(t, store.HandoffPolicy{AfterTasks: 1, ContextPercent: 70, ContextFile: file})
	pane := &recordingPane{}
	finishTask(t, h, "t-2")
	reportContext(t, file, 75)
	if got := wakeFor(t, h, pane, "n-1"); got != "guildhall: 1 unread (note 1 from guildmaster); handoff due (context 75%, 1 task finished)" {
		t.Fatalf("typed %q", got)
	}
	if got := dues(t, h); len(got) != 2 || got[0].Trigger != store.HandoffContext || got[1].Trigger != store.HandoffTasks {
		t.Fatalf("dues = %+v", got)
	}
}

// TestTriggersThatAreOffNeverHold: a zero threshold, and a context threshold with no report path because the
// runtime reports none, never cross.
func TestTriggersThatAreOffNeverHold(t *testing.T) {
	file := filepath.Join(t.TempDir(), session.ContextFile)
	for name, policy := range map[string]store.HandoffPolicy{
		"all off":           {ContextFile: file},
		"no context report": {ContextPercent: 70},
	} {
		h := handoffHarness(t, policy)
		pane := &recordingPane{}
		finishTask(t, h, "t-2")
		reportContext(t, file, 99)
		if got := wakeFor(t, h, pane, "n-1"); got != "guildhall: 1 unread (note 1 from guildmaster)" {
			t.Errorf("%s: typed %q", name, got)
		}
		if got := dues(t, h); len(got) != 0 {
			t.Errorf("%s: dues = %+v", name, got)
		}
	}
}

// TestAFailedWakeLeavesTheCrossingForTheNext: a wake that types nothing records no crossing, so the next wake
// carries it.
func TestAFailedWakeLeavesTheCrossingForTheNext(t *testing.T) {
	h := handoffHarness(t, store.HandoffPolicy{AfterTasks: 1})
	finishTask(t, h, "t-2")
	failing := &recordingPane{err: errors.New("herdr: socket gone")}
	sendDirector(t, h, "n-1", "note", "Carry on.")
	if w := mustWake(t, h, failing, "n-1", messaging.SenderGuildhall); w.Decision != store.WakeQueued {
		t.Fatalf("wake = %+v, want queued", w)
	}
	if got := dues(t, h); len(got) != 0 {
		t.Fatalf("a failed wake recorded %+v", got)
	}
	if got := wakeFor(t, h, &recordingPane{}, "n-2"); got != "guildhall: 2 unread (note 2 from guildmaster); handoff due (1 task finished)" {
		t.Fatalf("next wake typed %q", got)
	}
}

// TestWakeIdleTypesTheIdleCrossing: the idle line carries the counts and the idle trigger's next crossing, and
// any other trigger that holds; it types and records nothing for a Director it cannot wake.
func TestWakeIdleTypesTheIdleCrossing(t *testing.T) {
	h := handoffHarness(t, store.HandoffPolicy{AfterTasks: 1})
	pane := &recordingPane{}
	got, reason, err := messaging.WakeIdle(context.Background(), h.s, pane, 30*time.Minute)
	if err != nil || reason != "" || len(got) != 1 || got[0].Trigger != store.HandoffIdle || got[0].Crossing != 1 || got[0].Reason != "idle 30m0s" || got[0].MessageID != "" {
		t.Fatalf("WakeIdle = %+v, %q, %v", got, reason, err)
	}
	sendDirector(t, h, "q-1", "question", "Which branch?")
	finishTask(t, h, "t-2")
	if got, _, err = messaging.WakeIdle(context.Background(), h.s, pane, time.Hour); err != nil || len(got) != 2 || got[1].Trigger != store.HandoffIdle || got[1].Crossing != 2 {
		t.Fatalf("second WakeIdle = %+v, %v", got, err)
	}
	want := []string{
		"w1:p2 guildhall: 0 unread; handoff due (idle 30m0s)",
		"w1:p2 guildhall: 1 unread (question 1); handoff due (1 task finished, idle 1h0m0s)",
	}
	if !slices.Equal(pane.typed, want) {
		t.Fatalf("typed %q, want %q", pane.typed, want)
	}
	if d := dues(t, h); len(d) != 3 {
		t.Fatalf("dues = %+v; want both idle crossings and the task crossing", d)
	}

	for name, c := range map[string]struct {
		state  store.DirectorState
		pane   store.DirectorPane
		typer  *recordingPane
		reason string
	}{
		"paused":        {store.DirectorPaused, directorPane, &recordingPane{}, "paused"},
		"no pane":       {store.DirectorRunning, store.DirectorPane{}, &recordingPane{}, "no pane"},
		"typing failed": {store.DirectorRunning, directorPane, &recordingPane{err: errors.New("herdr: socket gone")}, "pane: herdr: socket gone"},
	} {
		h := wakeHarness(t, c.state, c.pane)
		got, reason, err := messaging.WakeIdle(context.Background(), h.s, c.typer, time.Minute)
		if err != nil || len(got) != 0 || reason != c.reason || len(c.typer.typed) != 0 {
			t.Errorf("%s: WakeIdle = %+v, %q, %v, typed %q", name, got, reason, err, c.typer.typed)
		}
		if d, err := h.s.HandoffDues(1); err != nil || len(d) != 0 {
			t.Errorf("%s: dues = %+v, %v", name, d, err)
		}
	}
}

func TestHandoffLine(t *testing.T) {
	line := "guildhall: 1 unread (note 1)"
	if got := messaging.HandoffLine(line, nil); got != line {
		t.Errorf("no dues: %q", got)
	}
	dues := []store.HandoffDue{{Reason: "context 72%"}, {Reason: "idle 5m0s"}}
	if got := messaging.HandoffLine(line, dues); got != line+"; handoff due (context 72%, idle 5m0s)" {
		t.Errorf("two dues: %q", got)
	}
	if got := messaging.WakeLine(map[string]int{}); got != "guildhall: 0 unread" {
		t.Errorf("WakeLine of nothing = %q", got)
	}
}
