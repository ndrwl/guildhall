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

// TestMaintenanceStatusFallsBackToRuns: a repository with no done hallmark task counts from its newest recorded
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
