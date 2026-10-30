package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// TestLastHallmark: the last hallmark is the newest done task by close whose workflow has a hallmark step and that
// recorded the finished document and a report, with its newest round's report and its receipts; a cancelled
// hallmark, an open one, one with no finished document, a feature task with a report-named document and a task whose
// workflow does not resolve are passed over. The newest open hallmark task is the running one.
func TestLastHallmark(t *testing.T) {
	def, feature := hallmarkDef(t), straight(t)
	gone := workflow.Revision{Name: "gone", Digest: "sha256:" + strings.Repeat("e", 64)}
	resolve := func(rev workflow.Revision) (*workflow.Definition, error) {
		switch rev.Name {
		case def.Name:
			return def, nil
		case feature.Name:
			return feature, nil
		}
		return nil, errors.New("unknown workflow")
	}
	r := interptest.NewRecords()
	create := func(id string, rev workflow.Revision, docs ...string) {
		t.Helper()
		if _, err := r.CreateTask("task:"+id, store.Task{Repository: "app", ID: id, Title: id, Workflow: rev, ConfigDigest: "sha256:" + strings.Repeat("c", 64)}); err != nil {
			t.Fatal(err)
		}
		for _, name := range docs {
			if _, err := r.PutDocument("doc:"+id+":"+name, store.DocumentRef{Repository: "app", TaskID: id, Name: name, Path: "tasks/app/" + id + "/" + name, SHA256: sum([]byte(name))}); err != nil {
				t.Fatal(err)
			}
		}
	}
	done := func(id string) {
		t.Helper()
		if _, err := r.SetTaskState("state:"+id, "app", id, store.TaskDone); err != nil {
			t.Fatal(err)
		}
	}
	// Created in id order, closed in another: h2 closes after h1 though created before it.
	create("h1", def.Revision(), "hallmark-1-report", "finished")
	create("h2", def.Revision(), "hallmark-1-report", "hallmark-2-report", "finished")
	create("h3", def.Revision(), "hallmark-1-report")
	create("h4", def.Revision(), "hallmark-1-report", "finished")
	create("f1", feature.Revision(), "hallmark-1-report", "finished")
	create("g1", gone, "hallmark-1-report", "finished")
	create("h5", def.Revision())
	create("h6", def.Revision())
	done("h1")
	done("h2")
	done("h3")
	if _, err := r.CancelTask("cancel:h4", "app", "h4", "Stopped."); err != nil {
		t.Fatal(err)
	}
	done("f1")
	done("g1")
	tasks, err := r.Tasks("app")
	if err != nil {
		t.Fatal(err)
	}
	closes, err := r.TaskCloses()
	if err != nil {
		t.Fatal(err)
	}

	h, err := interp.LastHallmark(tasks, closes, r.Documents, resolve, "")
	if err != nil || h == nil || h.Task.ID != "h2" || h.Report.Name != "hallmark-2-report" {
		t.Fatalf("LastHallmark = %+v, %v; want h2 with its round 2 report", h, err)
	}
	if c := closes[store.TaskRef{Repository: "app", ID: "h2"}]; h.Close != c || c.Created == 0 || c.Closed <= c.Created {
		t.Fatalf("h2 receipts = %+v, want %+v", h.Close, c)
	}
	if h, err := interp.LastHallmark(tasks, closes, r.Documents, resolve, "h2"); err != nil || h == nil || h.Task.ID != "h1" {
		t.Fatalf("LastHallmark except h2 = %+v, %v; want h1", h, err)
	}
	if h, err := interp.LastHallmark(tasks[:0], closes, r.Documents, resolve, ""); err != nil || h != nil {
		t.Fatalf("LastHallmark of no tasks = %+v, %v; want none", h, err)
	}
	if open := interp.OpenHallmark(tasks, closes, resolve); open == nil || open.ID != "h6" {
		t.Fatalf("OpenHallmark = %+v; want h6, the newest open hallmark", open)
	}
	done("h5")
	done("h6")
	tasks, _ = r.Tasks("app")
	if open := interp.OpenHallmark(tasks, closes, resolve); open != nil {
		t.Fatalf("OpenHallmark with none open = %+v; want none", open)
	}
}
