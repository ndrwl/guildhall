package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// createHallmarkTask creates repo/id bound to the shipped hallmark workflow, open.
func createHallmarkTask(t *testing.T, path, repo, id string) {
	t.Helper()
	def, err := workflow.Hallmark()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.CreateTask("task:"+repo+":"+id, store.Task{Repository: repo, ID: id, Title: "Hallmark", Workflow: def.Revision(),
			ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
	}); err != nil {
		t.Fatal(err)
	}
}

// finishHallmarkTask records repo/id's hallmark report and finished document, as the engine's finish does, and closes
// it done.
func finishHallmarkTask(t *testing.T, path, repo, id string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		for _, name := range []string{"hallmark-1-report", "finished"} {
			if _, err := s.PutDocument("doc:"+repo+":"+id+":"+name, store.DocumentRef{Repository: repo, TaskID: id, Name: name,
				Path: "tasks/" + repo + "/" + id + "/" + name, SHA256: digest.Of([]byte(name)).Hex()}); err != nil {
				return store.Receipt{}, err
			}
		}
		return s.SetTaskState("state:"+repo+":"+id, repo, id, store.TaskDone)
	}); err != nil {
		t.Fatal(err)
	}
}

// cancelHallmarkTask cancels repo/id.
func cancelHallmarkTask(t *testing.T, path, repo, id string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.CancelTask("cancel:"+repo+":"+id, repo, id, "Stopped.")
	}); err != nil {
		t.Fatal(err)
	}
}

// appHallmark returns status's maintenance entry for repository app.
func appHallmark(t *testing.T, st directorStatus) statusHallmark {
	t.Helper()
	if st.Maintenance == nil {
		t.Fatal("no maintenance object")
	}
	for _, h := range st.Maintenance.Hallmarks {
		if h.Repository == "app" {
			return h
		}
	}
	t.Fatalf("no hallmark entry for app in %+v", st.Maintenance.Hallmarks)
	return statusHallmark{}
}

// TestDirectorStatusHallmarkCadenceFollowsTheDoneTask: with no maintenance run recorded, a cancelled hallmark task
// leaves the hallmark due with no last task, and a done one with a finished document and a report makes it not due,
// counting the merges sent since it was created, whether during its run or after it (hallmark d78, H1).
func TestDirectorStatusHallmarkCadenceFollowsTheDoneTask(t *testing.T) {
	d := newRunDirector(t)
	startMaintenanceSession(t, d.store, 1, store.MaintenancePolicy{HallmarkAfterMerges: 3})
	sendMergedMessages(t, d.store, "app", "t-1", "m", 3)
	due := func(st directorStatus) bool {
		for _, it := range st.NeedsMe {
			if it.Kind == maintenanceKind && it.Repository == "app" {
				return true
			}
		}
		return false
	}
	if st := readStatus(t, d.store, d.state); !due(st) {
		t.Fatalf("3 merged: items %q; want the app hallmark due", maintenanceItems(st))
	}

	// A running hallmark holds the item back, and a cancelled one, failed or not, never resets the cadence.
	createHallmarkTask(t, d.store, "app", "h-1")
	st := readStatus(t, d.store, d.state)
	if h := appHallmark(t, st); due(st) || h.RunningTask != "h-1" || h.LastTask != "" || h.MergedSince != 3 {
		t.Fatalf("while h-1 runs: %+v, items %q; want it running and no item", h, maintenanceItems(st))
	}
	cancelHallmarkTask(t, d.store, "app", "h-1")
	st = readStatus(t, d.store, d.state)
	if h := appHallmark(t, st); !due(st) || h.RunningTask != "" || h.LastTask != "" || h.MergedSince != 3 {
		t.Fatalf("after a cancelled hallmark: %+v, items %q; want still due with no last task", h, maintenanceItems(st))
	}

	// A done hallmark, recorded nowhere else, resets it; the merge sent while it ran counts toward the next.
	createHallmarkTask(t, d.store, "app", "h-2")
	sendMergedMessages(t, d.store, "app", "t-1", "during", 1)
	finishHallmarkTask(t, d.store, "app", "h-2")
	st = readStatus(t, d.store, d.state)
	if h := appHallmark(t, st); due(st) || h.LastTask != "h-2" || h.LastAt.IsZero() || h.MergedSince != 1 || *h.MergesUntilDue != 2 {
		t.Fatalf("after a done hallmark: %+v, items %q; want h-2 last with 1 merged since and not due", h, maintenanceItems(st))
	}
	sendMergedMessages(t, d.store, "app", "t-1", "after", 2)
	st = readStatus(t, d.store, d.state)
	want := "app: hallmark due: 3 merged since the last | guildhall task new --repo app --kind hallmark --title <text> <id>"
	if got := maintenanceItems(st); len(got) != 1 || got[0] != want {
		t.Fatalf("items = %q\nwant %q", got, want)
	}

	// readStatus has no config, so h-2 resolved against the shipped hallmark workflow by digest; with the config it
	// resolves through the repository's settings to the same task.
	code, out, stderr := runCLI(t, "director", "status", "--json", "--store", d.store, "--state", d.state, "--config", d.config)
	if code != 0 || !strings.Contains(out, `"repository":"app","after_merges":3,"last_task":"h-2"`) {
		t.Fatalf("status with the config: exit %d, stderr %s\n%s", code, stderr, out)
	}
}

// recordHallmarkReport records repo/id's hallmark report and finished document, as the engine's finish does, and
// leaves the task open, as a hallmark waiting on its follow-ups decision is.
func recordHallmarkReport(t *testing.T, path, repo, id string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		var rc store.Receipt
		for _, name := range []string{"hallmark-1-report", "finished"} {
			r, err := s.PutDocument("doc:"+repo+":"+id+":"+name, store.DocumentRef{Repository: repo, TaskID: id, Name: name,
				Path: "tasks/" + repo + "/" + id + "/" + name, SHA256: digest.Of([]byte(name)).Hex()})
			if err != nil {
				return store.Receipt{}, err
			}
			rc = r
		}
		return rc, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// openFollowUps opens repo/id's follow-ups decision, as the engine's finish does; the task stays open until it is
// answered.
func openFollowUps(t *testing.T, path, repo, id string) {
	t.Helper()
	if _, err := withStore(path, false, func(s *store.Store) (store.Receipt, error) {
		return s.OpenDecision("follow-ups:"+repo+":"+id, store.Decision{Repository: repo, TaskID: id, ID: interp.FollowUpsDecision,
			Kind: store.DecisionFollowUps, Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + digest.Of([]byte("hallmark-1-report")).Hex()},
			Options: []string{"pick", "none"}, Question: "Pick the report's follow-ups."})
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDirectorStatusHallmarkWaitsForFollowUps: a hallmark that recorded its report stays open while its follow-ups
// decision is unanswered, deferred or not, so it is the running_task and holds the hallmark due item back however
// many merges land, with no awaiting_follow_ups field; once it is answered, pick or none, and the task closes, merges
// count only after the answer and the item returns at the threshold (d119, d121).
func TestDirectorStatusHallmarkWaitsForFollowUps(t *testing.T) {
	for _, answer := range []string{"pick\nf-1", "none"} {
		t.Run(strings.Fields(answer)[0], func(t *testing.T) {
			d := newRunDirector(t)
			startMaintenanceSession(t, d.store, 1, store.MaintenancePolicy{HallmarkAfterMerges: 2})
			createHallmarkTask(t, d.store, "app", "h-1")
			sendMergedMessages(t, d.store, "app", "t-1", "during", 1)
			recordHallmarkReport(t, d.store, "app", "h-1")
			openFollowUps(t, d.store, "app", "h-1")
			sendMergedMessages(t, d.store, "app", "t-1", "before", 2)

			// Open: the merges reach the threshold, but the open hallmark is the running task and holds the item.
			st := readStatus(t, d.store, d.state)
			if h := appHallmark(t, st); len(maintenanceItems(st)) != 0 || h.RunningTask != "h-1" || h.LastTask != "" || h.MergedSince != 3 || *h.MergesUntilDue != 0 {
				t.Fatalf("while the follow-ups are open: %+v, items %q; want h-1 running and the item held", h, maintenanceItems(st))
			}
			code, out, stderr := runCLI(t, "director", "status", "--json", "--store", d.store, "--state", d.state)
			if code != 0 || !strings.Contains(out, `"running_task":"h-1"`) || strings.Contains(out, "awaiting_follow_ups") {
				t.Fatalf("status --json: exit %d, stderr %s\n%s\nwant running_task h-1 and no awaiting_follow_ups", code, stderr, out)
			}

			// Deferred: a deferral does not answer the decision, so the hallmark stays open and still holds.
			createHallmarkTask(t, d.store, "other", "o-1")
			if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
				return s.DeferDecision("defer-follow-ups", "app", "h-1", interp.FollowUpsDecision, "After o-1.",
					[]store.TaskRef{{Repository: "other", ID: "o-1"}}, 0)
			}); err != nil {
				t.Fatal(err)
			}
			st = readStatus(t, d.store, d.state)
			if h := appHallmark(t, st); len(maintenanceItems(st)) != 0 || h.RunningTask != "h-1" {
				t.Fatalf("while the follow-ups are deferred: %+v, items %q; want h-1 still running", h, maintenanceItems(st))
			}

			// Answered and closed: the merges before the answer stop counting, and the item returns after 2 more.
			if _, err := withStore(d.store, false, func(s *store.Store) (store.Receipt, error) {
				return s.AnswerDecision("answer-follow-ups", "app", "h-1", interp.FollowUpsDecision, answer)
			}); err != nil {
				t.Fatal(err)
			}
			finishHallmarkTask(t, d.store, "app", "h-1")
			st = readStatus(t, d.store, d.state)
			if h := appHallmark(t, st); len(maintenanceItems(st)) != 0 || h.RunningTask != "" || h.LastTask != "h-1" || h.MergedSince != 0 || *h.MergesUntilDue != 2 {
				t.Fatalf("after the answer: %+v, items %q; want h-1 last and the count restarted at 0", h, maintenanceItems(st))
			}
			sendMergedMessages(t, d.store, "app", "t-1", "after", 1)
			st = readStatus(t, d.store, d.state)
			if h := appHallmark(t, st); len(maintenanceItems(st)) != 0 || h.MergedSince != 1 {
				t.Fatalf("1 merged after the answer: %+v, items %q; want 1 counted and not due", h, maintenanceItems(st))
			}
			sendMergedMessages(t, d.store, "app", "t-1", "later", 1)
			st = readStatus(t, d.store, d.state)
			want := "app: hallmark due: 2 merged since the last | guildhall task new --repo app --kind hallmark --title <text> <id>"
			if got := maintenanceItems(st); len(got) != 1 || got[0] != want {
				t.Fatalf("items = %q\nwant %q", got, want)
			}
		})
	}
}

// TestMaintenanceStatusFollowUps: merges count only after the last hallmark's follow-ups answer receipt, and a
// running hallmark, such as one waiting on its own follow-ups, holds the item back at the threshold (d119, d121).
func TestMaintenanceStatusFollowUps(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	v := store.DirectorView{Session: &store.DirectorSession{Number: 2, Maintenance: store.MaintenancePolicy{HallmarkAfterMerges: 2}},
		Maintenance: store.MaintenanceView{Baseline: 1, BaselineReceipt: 10, Repositories: []store.RepositoryMaintenance{
			{Repository: "app", Merged: 3, MergedSequences: []uint64{12, 21, 22}},
			{Repository: "web", Merged: 3, MergedSequences: []uint64{12, 21, 31}},
		}}}
	app := &interp.Hallmark{Task: store.Task{Repository: "app", ID: "a-0"}, Close: store.TaskClose{Created: 11, Closed: 20, At: at}}
	web := &interp.Hallmark{Task: store.Task{Repository: "web", ID: "w-0"}, Close: store.TaskClose{Created: 11, Closed: 26, At: at}}
	m, items := maintenanceStatus(v, map[string]repoHallmarks{
		"app": {last: app, running: &store.Task{Repository: "app", ID: "a-1"}, followUpsAnswered: 19},
		"web": {last: web, followUpsAnswered: 25},
	})
	if h := m.Hallmarks[0]; h.RunningTask != "a-1" || h.LastTask != "a-0" || h.MergedSince != 2 || *h.MergesUntilDue != 0 {
		t.Fatalf("app = %+v; want a-1 running, counting the 2 merges after a-0's answer", h)
	}
	if h := m.Hallmarks[1]; h.RunningTask != "" || h.MergedSince != 1 || *h.MergesUntilDue != 1 {
		t.Fatalf("web = %+v; want the 1 merge after the answer counted", h)
	}
	if got := maintenanceItems(directorStatus{NeedsMe: items}); len(got) != 0 {
		t.Fatalf("items = %q; want none", got)
	}
}

// TestMaintenanceStatusFallsBackToRuns:a repository with no done hallmark task counts from its newest recorded
// hallmark run, as before; one with a done hallmark task counts from that task's creation, even when a run is newer.
func TestMaintenanceStatusFallsBackToRuns(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	v := store.DirectorView{Session: &store.DirectorSession{Number: 2, Maintenance: store.MaintenancePolicy{HallmarkAfterMerges: 2}},
		Maintenance: store.MaintenanceView{Baseline: 1, BaselineReceipt: 10, Repositories: []store.RepositoryMaintenance{
			{Repository: "app", LastHallmark: &store.MaintenanceRun{Kind: store.MaintenanceHallmark, Repository: "app", Task: "t-1", Receipt: 20, Time: at},
				Merged: 2, MergedSequences: []uint64{15, 21, 22}},
			{Repository: "web", LastHallmark: &store.MaintenanceRun{Kind: store.MaintenanceHallmark, Repository: "web", Task: "w-1", Receipt: 30, Time: at},
				Merged: 0, MergedSequences: []uint64{12, 25}},
		}}}
	done := &interp.Hallmark{Task: store.Task{Repository: "web", ID: "w-0"}, Close: store.TaskClose{Created: 11, Closed: 28, At: at.Add(time.Hour)}}
	m, items := maintenanceStatus(v, map[string]repoHallmarks{"web": {last: done}})
	if app := m.Hallmarks[0]; app.LastTask != "t-1" || !app.LastAt.Equal(at) || app.MergedSince != 2 {
		t.Fatalf("app = %+v; want the run's task and count", app)
	}
	if web := m.Hallmarks[1]; web.LastTask != "w-0" || !web.LastAt.Equal(at.Add(time.Hour)) || web.MergedSince != 2 {
		t.Fatalf("web = %+v; want the done task, counting the 2 merges after its creation", web)
	}
	want := []string{
		"app: hallmark due: 2 merged since the last | guildhall task new --repo app --kind hallmark --title <text> <id>",
		"web: hallmark due: 2 merged since the last | guildhall task new --repo web --kind hallmark --title <text> <id>",
	}
	if got := maintenanceItems(directorStatus{NeedsMe: items}); !slices.Equal(got, want) {
		t.Fatalf("items = %q\nwant %q", got, want)
	}
}
