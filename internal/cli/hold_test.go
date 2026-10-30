package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// holdCopy is a store and state directory with open app tasks d-1 and d-2 for holds to wait on; t-1 with build
// attempt 1 failed and parked until both merge; t-3 with open decision q-1 deferred until d-1 merges; follow-up f1
// until d-2 merges; and Director session 1 running in a Herdr pane.
func holdCopy(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path, state := filepath.Join(dir, "director.store"), filepath.Join(dir, "state")
	s, err := store.Open(path, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	d1, d2 := store.TaskRef{Repository: "app", ID: "d-1"}, store.TaskRef{Repository: "app", ID: "d-2"}
	for _, id := range []string{"d-1", "d-2", "t-1", "t-3"} {
		if _, err := s.CreateTask("create-"+id, store.Task{Repository: "app", ID: id, Title: "Hold " + id, Workflow: def.Revision(),
			ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []func() (store.Receipt, error){
		func() (store.Receipt, error) { return s.StartAttempt("start-t-1", "app", "t-1", "build") },
		func() (store.Receipt, error) {
			return s.FinishAttempt("fail-t-1", "app", "t-1", "build", 1, store.AttemptFailed)
		},
		func() (store.Receipt, error) {
			return s.ParkAttempt("park-t-1", "app", "t-1", "build", 1, "Rebase after the store change.", 0, []store.TaskRef{d1, d2})
		},
		func() (store.Receipt, error) {
			return s.OpenDecision("open-q-1", store.Decision{Repository: "app", TaskID: "t-3", ID: "q-1", Kind: store.DecisionQuestion, Question: "Ship it?"})
		},
		func() (store.Receipt, error) {
			return s.DeferDecision("defer-q-1", "app", "t-3", "q-1", "Ask once d-1 is in.", []store.TaskRef{d1}, 0)
		},
	} {
		if _, err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	writeNext(t, state, "t-1", `{"wait":"director","step":"build","attempt":1}`+"\n")
	// The session has a pane, so needs_me holds no no_pane item and lists only what the holds release.
	recordPanedSession(t, path, herdrPane, false, store.DirectorRunning)
	if _, err := withStore(path, false, func(s *store.Store) (store.FollowUp, error) {
		f, _, err := s.AddFollowUp(1, "Tell the Guildmaster d-2 is in.", []store.TaskRef{d2})
		return f, err
	}); err != nil {
		t.Fatal(err)
	}
	return path, state
}

// setState closes a task of the store at path.
func setState(t *testing.T, path, id string, state store.TaskState) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.SetTaskState("close-"+id, "app", id, state)
	}); err != nil {
		t.Fatal(err)
	}
}

func readStatus(t *testing.T, path, state string) directorStatus {
	t.Helper()
	st, err := readDirectorStatus(context.Background(), path, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// needsMe lists status's needs_me items as "<kind> <task or follow-up>".
func needsMe(st directorStatus) []string {
	var out []string
	for _, it := range st.NeedsMe {
		out = append(out, strings.TrimSpace(it.Kind+" "+it.TaskID+it.FollowUp))
	}
	return out
}

// TestDirectorStatusHolds: while their tasks are open, a held park counts as parked with what it waits for, and a
// deferred decision and a follow-up are listed as held and left out of needs_me and the Lanterns counts. A merge
// releases the decision with the merge named; a held park waits for every task it names; a cancelled task releases
// the park, as a retry with its park reason, and the follow-up, which is counted as attention and as the
// Director's on resume. Answering the decision, finishing the follow-up and retrying the attempt each end a hold.
func TestDirectorStatusHolds(t *testing.T) {
	path, state := holdCopy(t)

	st := readStatus(t, path, state)
	if got := needsMe(st); len(got) != 0 {
		t.Errorf("needs_me while every hold waits = %v, want none", got)
	}
	if len(st.Parked) != 1 || !slices.Equal(st.Parked[0].UntilMerged, []string{"app/d-1", "app/d-2"}) ||
		!slices.Equal(st.Parked[0].WaitingFor, []string{"app/d-1", "app/d-2"}) {
		t.Errorf("parked = %+v, want t-1 waiting for d-1 and d-2", st.Parked)
	}
	want := []statusHeld{
		{Kind: "decision", Repository: "app", TaskID: "t-3", Item: "q-1", Reason: "Ask once d-1 is in.", UntilMerged: []string{"app/d-1"},
			WaitingFor: []string{"app/d-1"}, Command: "guildhall decide undefer --repo app --task t-3 --decision q-1"},
		{Kind: "follow-up", Item: "f1", Reason: "Tell the Guildmaster d-2 is in.", UntilMerged: []string{"app/d-2"}, WaitingFor: []string{"app/d-2"},
			Command: "guildhall director follow-up done f1"},
	}
	if fmt.Sprint(st.Held) != fmt.Sprint(want) {
		t.Errorf("held = %+v\nwant %+v", st.Held, want)
	}
	if len(st.Decisions) != 1 || !st.Decisions[0].Deferred {
		t.Errorf("decisions = %+v, want q-1 deferred", st.Decisions)
	}
	if c := lanternCounts(st); c.Decisions != 0 || c.Attention != 0 {
		t.Errorf("lantern counts while held = %+v, want no decisions and no attention", c)
	}
	if len(st.released) != 0 {
		t.Errorf("released while waiting = %+v", st.released)
	}

	setState(t, path, "d-1", store.TaskDone)
	st = readStatus(t, path, state)
	if got := needsMe(st); !slices.Equal(got, []string{"decision t-3"}) {
		t.Errorf("needs_me after d-1 merged = %v, want only decision t-3", got)
	}
	if r := st.NeedsMe[0].Reason; !strings.HasPrefix(r, "Ship it?; released: app/d-1 merged 20") || !strings.HasSuffix(r, "; deferral reason: Ask once d-1 is in.") {
		t.Errorf("released decision reason = %q", r)
	}
	if len(st.Parked) != 1 || !slices.Equal(st.Parked[0].WaitingFor, []string{"app/d-2"}) {
		t.Errorf("parked after d-1 merged = %+v, want t-1 still waiting for d-2", st.Parked)
	}
	if c := lanternCounts(st); c.Decisions != 1 {
		t.Errorf("lantern decisions after release = %d, want 1", c.Decisions)
	}

	setState(t, path, "d-2", store.TaskCancelled)
	st = readStatus(t, path, state)
	if got := needsMe(st); !slices.Equal(got, []string{"retry t-1", "decision t-3", "follow-up f1", "director t-1"}) {
		t.Errorf("needs_me after d-2 was cancelled = %v", got)
	}
	if r := st.NeedsMe[0].Reason; r != "attempt 1 of build failed; dependency cancelled: app/d-2; park reason: Rebase after the store change." {
		t.Errorf("released park's retry reason = %q", r)
	}
	if r := st.NeedsMe[2].Reason; r != "Tell the Guildmaster d-2 is in.; dependency cancelled: app/d-2" || st.NeedsMe[2].Command != "guildhall director follow-up done f1" {
		t.Errorf("follow-up item = %+v", st.NeedsMe[2])
	}
	if len(st.Parked) != 0 || len(st.Held) != 0 {
		t.Errorf("after every release: parked %+v, held %+v", st.Parked, st.Held)
	}
	if c := lanternCounts(st); c.Attention != 2 {
		t.Errorf("lantern attention = %d, want the retry and the follow-up", c.Attention)
	}
	if n := waitingKinds(st.NeedsMe); n[followUpKind] != 1 || n["retry"] != 1 || n[decisionKind] != 0 {
		t.Errorf("waitingKinds = %v, want the follow-up and the retry counted", n)
	}
	if len(st.released) != 3 {
		t.Errorf("released = %+v, want the park, the decision and the follow-up", st.released)
	}

	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.AnswerDecision("answer-q-1", "app", "t-3", "q-1", "yes"); err != nil {
			return store.Receipt{}, err
		}
		if _, _, err := s.DoneFollowUp(1, "f1"); err != nil {
			return store.Receipt{}, err
		}
		return s.StartAttempt("retry-t-1", "app", "t-1", "build")
	}); err != nil {
		t.Fatal(err)
	}
	st = readStatus(t, path, state)
	if len(st.released) != 0 || slices.ContainsFunc(st.NeedsMe, func(it needsMeItem) bool { return it.Kind == decisionKind || it.Kind == followUpKind }) {
		t.Errorf("after answer, done and retry: released %+v, needs_me %v", st.released, needsMe(st))
	}
}

// TestHoldsRoundWakesOncePerRelease: the holds watcher sends nothing while holds wait, one hold-released message and
// one wake per release however many rounds see it, and nothing for a hold whose item was dealt with first.
func TestHoldsRoundWakesOncePerRelease(t *testing.T) {
	path, state := holdCopy(t)
	var woken []string
	wake := func(id string) error { woken = append(woken, id); return nil }
	var logged []string
	logf := func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }

	holdsRound(path, state, wake, logf)
	if len(woken) != 0 {
		t.Fatalf("woken while every hold waits: %v", woken)
	}
	setState(t, path, "d-1", store.TaskDone)
	holdsRound(path, state, wake, logf)
	holdsRound(path, state, wake, logf)
	if !slices.Equal(woken, []string{"hold-released.decision.app.t-3.q-1.0"}) {
		t.Fatalf("woken after d-1 merged = %v, want the decision's release once", woken)
	}
	// The park is retried before its last task merges: its release no longer matters.
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.StartAttempt("retry-t-1", "app", "t-1", "build")
	}); err != nil {
		t.Fatal(err)
	}
	setState(t, path, "d-2", store.TaskDone)
	holdsRound(path, state, wake, logf)
	holdsRound(path, state, wake, logf)
	if !slices.Equal(woken, []string{"hold-released.decision.app.t-3.q-1.0", "hold-released.follow-up.f1"}) {
		t.Fatalf("woken = %v, want the decision and then the follow-up, once each", woken)
	}
	m, err := withStore(path, true, func(s *store.Store) (store.Message, error) { return s.Message("hold-released.follow-up.f1") })
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != store.MessageHoldReleased || m.Repository != "app" || m.TaskID != "d-2" ||
		!strings.HasPrefix(m.Text, "Follow-up f1 is released (released: app/d-2 merged ") || !strings.Contains(m.Text, "`guildhall director follow-up done f1`") {
		t.Errorf("follow-up message = %+v", m)
	}
	unread, err := withStore(path, true, func(s *store.Store) (map[string]int, error) { return s.DirectorUnread() })
	if err != nil || unread[store.MessageHoldReleased] != 2 {
		t.Errorf("unread = %v, %v; want two hold-released messages counted by type", unread, err)
	}
}

// TestHoldsRoundWaitsForARunningSession: a paused session's watcher sends nothing.
func TestHoldsRoundWaitsForARunningSession(t *testing.T) {
	path, state := holdCopy(t)
	setState(t, path, "d-1", store.TaskDone)
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		d, err := s.DirectorSession(1)
		if err != nil {
			return store.Receipt{}, err
		}
		return s.SetDirectorState(1, store.DirectorPaused, d.StateSequence)
	}); err != nil {
		t.Fatal(err)
	}
	var woken []string
	holdsRound(path, state, func(id string) error { woken = append(woken, id); return nil }, func(string, ...any) {})
	if len(woken) != 0 {
		t.Errorf("woken while paused: %v", woken)
	}
}

// TestStepParkUntilMerged: step park --until-merged records the tasks and prints them, an identical park replays,
// a park with another set while parked is refused quoting the prior one, and a task already merged is refused.
func TestStepParkUntilMerged(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Dependency", "t-2")
	newTask(t, d.store, d.config, "app", "Merged", "t-3")
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartAttempt("m-1", "app", "t-1", "plan"); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.SetTaskState("m-3", "app", "t-3", store.TaskDone); err != nil {
			return store.Receipt{}, err
		}
		return s.FinishAttempt("m-2", "app", "t-1", "plan", 1, store.AttemptFailed)
	}); err != nil {
		t.Fatal(err)
	}
	park := func(extra ...string) []string {
		return append([]string{"step", "park"}, d.flags(append([]string{"--task", "t-1", "--step", "plan", "--attempt", "1", "--reason", "After t-2."}, extra...)...)...)
	}
	parked := `{"repository":"app","task_id":"t-1","step":"plan","attempt":1,"reason":"After t-2.","parked":true,"cycle":0,"until_merged":[{"repository":"app","id":"t-2"}]}` + "\n"
	if code, out, stderr := d.run(t, park("--until-merged", "app/t-3")...); code != 1 || !strings.Contains(stderr, "app/t-3 already merged; act now instead") {
		t.Errorf("park until a merged task: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := d.run(t, park("--until-merged", "app/t-9")...); code != 1 || !strings.Contains(stderr, "not found") {
		t.Errorf("park until an unknown task: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := d.run(t, park("--until-merged", "t-2")...); code != 2 || out != "" {
		t.Errorf("park with a malformed task: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := d.run(t, park("--until-merged", "app/t-2")...); code != 0 || out != parked {
		t.Fatalf("park: exit %d, %q, stderr %s; want %s", code, out, stderr, parked)
	}
	before := lastSequence(t, d.store)
	if code, out, _ := d.run(t, park("--until-merged", "app/t-2")...); code != 0 || out != parked || lastSequence(t, d.store) != before {
		t.Errorf("identical park: exit %d, %q; want a replay writing nothing", code, out)
	}
	want := `attempt 1 of plan of app/t-1 is already parked, with reason "After t-2." until app/t-2 merges; unpark it first`
	if code, _, stderr := d.run(t, park()...); code != 1 || !strings.Contains(stderr, want) {
		t.Errorf("park without the set while held: exit %d, stderr %s; want %q", code, stderr, want)
	}
}

// TestDecideDeferAndUndefer: decide defer records the deferral and prints it, replays an identical one, refuses
// another reason while deferred, and undefer lifts it once.
func TestDecideDeferAndUndefer(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Dependency", "t-2")
	if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
		return s.OpenDecision("open", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Ship it?"})
	}); err != nil {
		t.Fatal(err)
	}
	flags := []string{"--store", d.store, "--repo", "app", "--task", "t-1", "--decision", "q-1"}
	deferArgs := func(reason string) []string {
		return append(append([]string{"decide", "defer"}, flags...), "--until-merged", "app/t-2", "--reason", reason)
	}
	deferred := `{"repository":"app","task_id":"t-1","decision":"q-1","reason":"After t-2.","until_merged":[{"repository":"app","id":"t-2"}],"deferred":true,"cycle":0}` + "\n"
	if code, out, stderr := runCLI(t, deferArgs("After t-2.")...); code != 0 || out != deferred {
		t.Fatalf("defer: exit %d, %q, stderr %s; want %s", code, out, stderr, deferred)
	}
	before := lastSequence(t, d.store)
	if code, out, _ := runCLI(t, deferArgs("After t-2.")...); code != 0 || out != deferred || lastSequence(t, d.store) != before {
		t.Errorf("identical defer: exit %d, %q; want a replay writing nothing", code, out)
	}
	if code, _, stderr := runCLI(t, deferArgs("Another.")...); code != 1 || !strings.Contains(stderr, `already deferred, with reason "After t-2." until app/t-2 merges`) {
		t.Errorf("defer with another reason: exit %d, stderr %s", code, stderr)
	}
	undeferred := `{"repository":"app","task_id":"t-1","decision":"q-1","reason":"After t-2.","until_merged":[{"repository":"app","id":"t-2"}],"deferred":false,"cycle":1}` + "\n"
	undefer := append([]string{"decide", "undefer"}, flags...)
	if code, out, stderr := runCLI(t, undefer...); code != 0 || out != undeferred {
		t.Errorf("undefer: exit %d, %q, stderr %s; want %s", code, out, stderr, undeferred)
	}
	if code, _, stderr := runCLI(t, undefer...); code != 1 || !strings.Contains(stderr, "is not deferred") {
		t.Errorf("second undefer: exit %d, stderr %s", code, stderr)
	}
	for name, args := range map[string][]string{
		"defer without until":   append(append([]string{"decide", "defer"}, flags...), "--reason", "r"),
		"defer without reason":  append(append([]string{"decide", "defer"}, flags...), "--until-merged", "app/t-2"),
		"defer with a bad task": append(append([]string{"decide", "defer"}, flags...), "--until-merged", "t-2", "--reason", "r"),
		"undefer with a reason": append(append([]string{"decide", "undefer"}, flags...), "--reason", "r"),
	} {
		if code, out, _ := runCLI(t, args...); code != 2 || out != "" {
			t.Errorf("%s: exit %d, %q; want 2", name, code, out)
		}
	}
}

// TestDirectorFollowUpCommands: add and done print a one-line receipt as the newest session, and list prints the
// follow-ups not done with what each waits for or what released it, or "No follow-ups.".
func TestDirectorFollowUpCommands(t *testing.T) {
	d := newRunDirector(t)
	newTask(t, d.store, d.config, "app", "Dependency", "t-2")
	run := func(args ...string) (int, string, string) {
		return runCLI(t, append(append([]string{"director", "follow-up"}, args...), "--store", d.store)...)
	}
	if code, _, stderr := run("add", "--until-merged", "app/t-2", "x"); code != 1 || !strings.Contains(stderr, "no Director session") {
		t.Fatalf("add with no session: exit %d, stderr %s", code, stderr)
	}
	recordSession(t, d.store, false, store.DirectorRunning)
	if code, out, stderr := run("add", "--until-merged", "app/t-2", "--until-merged", "app/t-1", "Rebase d20."); code != 0 || out != "f1 added\n" {
		t.Fatalf("add: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := run("add", "Tell the Guildmaster.", "--until-merged", "app/t-2"); code != 0 || out != "f2 added\n" {
		t.Fatalf("second add: exit %d, %q, stderr %s", code, out, stderr)
	}
	setState(t, d.store, "t-2", store.TaskDone)
	want := "- f1 until app/t-2, app/t-1 (waiting for app/t-1): Rebase d20.\n- f2 until app/t-2 [released: app/t-2 merged]: Tell the Guildmaster.\n"
	if code, out, stderr := run("list"); code != 0 || out != want {
		t.Errorf("list: exit %d, stderr %s\n%s\nwant\n%s", code, stderr, out, want)
	}
	if code, out, stderr := run("done", "f2"); code != 0 || out != "f2 done\n" {
		t.Errorf("done: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, stderr := run("done", "f1"); code != 0 || out != "f1 done\n" {
		t.Errorf("done f1: exit %d, %q, stderr %s", code, out, stderr)
	}
	if code, out, _ := run("list"); code != 0 || out != "No follow-ups.\n" {
		t.Errorf("list of none: exit %d, %q", code, out)
	}
	for name, c := range map[string]struct {
		args []string
		code int
	}{
		"add without until":     {[]string{"add", "x"}, 2},
		"add without text":      {[]string{"add", "--until-merged", "app/t-1"}, 2},
		"add until a done task": {[]string{"add", "--until-merged", "app/t-2", "x"}, 1},
		"add a multi-line text": {[]string{"add", "--until-merged", "app/t-1", "a\nb"}, 1},
		"done again replays":    {[]string{"done", "f1"}, 0},
		"done unknown":          {[]string{"done", "f9"}, 1},
		"unknown command":       {[]string{"remove", "f1"}, 2},
	} {
		if code, _, _ := run(c.args...); code != c.code {
			t.Errorf("%s: exit %d, want %d", name, code, c.code)
		}
	}
}

// TestDirectorStartStartupListsHeldItems: startup.md gives a held park its until suffix and lists a deferred
// decision in its own held section; a follow-up's line names its id.
func TestDirectorStartStartupListsHeldItems(t *testing.T) {
	dir := directorCopy(t, "[director]\nadapter = \"fake\"\n", "/exit")
	if code, out, stderr := runDirectorCmd(t, "start", "--copy", dir); code != 0 {
		t.Fatalf("first session: exit %d\n%s\n%s", code, out, stderr)
	}
	storePath := filepath.Join(dir, "director.store")
	newTask(t, storePath, filepath.Join(dir, "config.toml"), "app", "Held", "t-1")
	newTask(t, storePath, filepath.Join(dir, "config.toml"), "app", "Dependency", "t-2")
	until := []store.TaskRef{{Repository: "app", ID: "t-2"}}
	withTestStore(t, dir, func(s *store.Store) (store.Receipt, error) {
		if _, err := s.StartAttempt("start", "app", "t-1", "build"); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.FinishAttempt("fail", "app", "t-1", "build", 1, store.AttemptFailed); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.OpenDecision("open", store.Decision{Repository: "app", TaskID: "t-1", ID: "q-1", Kind: store.DecisionQuestion, Question: "Ship?"}); err != nil {
			return store.Receipt{}, err
		}
		if _, err := s.DeferDecision("defer", "app", "t-1", "q-1", "After t-2.", until, 0); err != nil {
			return store.Receipt{}, err
		}
		return s.ParkAttempt("park", "app", "t-1", "build", 1, "Rebase first.", 0, until)
	})
	writeNext(t, filepath.Join(dir, "state"), "t-1", `{"wait":"director","step":"build","attempt":1}`+"\n")
	if code, out, stderr := runDirectorCmd(t, "start", "fake", "--copy", dir); code != 0 {
		t.Fatalf("second session: exit %d\n%s\n%s", code, out, stderr)
	}
	b, err := os.ReadFile(filepath.Join(dir, "director", "session-2", "startup.md"))
	if err != nil {
		t.Fatal(err)
	}
	startup := string(b)
	for _, line := range []string{
		"- parked: attempt 1 of build of app/t-1 failed; reason: Rebase first.; retry with `guildhall step retry --repo app --task t-1 --step build --attempt 1` until app/t-2\n",
		"## Held\n",
		"- held: decision q-1 of app/t-1 until app/t-2 (waiting for app/t-2): After t-2.; lift with `guildhall decide undefer --repo app --task t-1 --decision q-1`\n",
	} {
		if !strings.Contains(startup, line) {
			t.Errorf("startup.md lacks %q:\n%s", line, startup)
		}
	}
	follow := statusHeld{Kind: followUpKind, Item: "f1", Reason: "Tell the\nGuildmaster.", UntilMerged: []string{"app/t-2", "app/t-3"}, WaitingFor: []string{"app/t-3"},
		Command: "guildhall director follow-up done f1"}
	if got, want := heldLine(follow), "held: follow-up f1 until app/t-2, app/t-3 (waiting for app/t-3): Tell the Guildmaster.; lift with `guildhall director follow-up done f1`"; got != want {
		t.Errorf("follow-up line = %q\nwant %q", got, want)
	}
}
