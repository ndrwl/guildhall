package store

import (
	"errors"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// linkFixture is a store with an open task app/t-1 and two documents to link: app/src plan-5-spec and lib/other's
// notes, in another repository, on a done task.
func linkFixture(t *testing.T) (*Store, DocumentRef, DocumentRef) {
	t.Helper()
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	mustCreate(t, s, "m-src", sampleTask("app", "src"))
	mustCreate(t, s, "m-other", sampleTask("lib", "other"))
	spec := DocumentRef{Repository: "app", TaskID: "src", Name: "plan-5-spec", Path: "tasks/app/src/plan-5/spec.md", SHA256: strings.Repeat("a", 64)}
	notes := DocumentRef{Repository: "lib", TaskID: "other", Name: "research-1-notes", Path: "tasks/lib/other/research-1/notes.md", SHA256: strings.Repeat("b", 64)}
	for _, d := range []DocumentRef{spec, notes} {
		if _, err := s.PutDocument("doc-"+d.TaskID, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetTaskState("close-other", "lib", "other", TaskDone); err != nil {
		t.Fatal(err)
	}
	return s, spec, notes
}

// name is the part of a document reference AddTaskLink reads.
func name(d DocumentRef) DocumentRef {
	return DocumentRef{Repository: d.Repository, TaskID: d.TaskID, Name: d.Name}
}

func TestAddTaskLink(t *testing.T) {
	s, spec, notes := linkFixture(t)
	type step struct {
		m     string
		op    TaskLinkOp
		src   DocumentRef
		prior int
	}
	var receipts []Receipt
	steps := []step{
		{"link-1", TaskLinkAdd, spec, 0},
		{"link-2", TaskLinkAdd, notes, 0},
		{"link-3", TaskLinkRemove, spec, 1},
		{"link-4", TaskLinkAdd, spec, 2},
	}
	for i, st := range steps {
		rc, err := s.AddTaskLink(st.m, "app", "t-1", st.op, name(st.src), st.prior, "Guildmaster: read "+st.src.Name+".")
		if err != nil || rc.Record != TaskLinkRecord(i+1) || rc.Operation != "add-task-link" || rc.Repository != "app" || rc.TaskID != "t-1" {
			t.Fatalf("AddTaskLink %d = %+v, %v", i+1, rc, err)
		}
		receipts = append(receipts, rc)
	}
	if replay, err := s.AddTaskLink("link-1", "app", "t-1", TaskLinkAdd, name(spec), 0, "Guildmaster: read plan-5-spec."); err != nil || replay != receipts[0] {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, receipts[0])
	}
	got, err := s.TaskLinks("app", "t-1")
	if err != nil || len(got) != len(steps) {
		t.Fatalf("TaskLinks = %+v, %v", got, err)
	}
	for i, l := range got {
		w := TaskLink{Repository: "app", TaskID: "t-1", Number: i + 1, Op: steps[i].op, Source: steps[i].src,
			Text: "Guildmaster: read " + steps[i].src.Name + ".", Author: TaskLinkAuthor, AddedAt: receipts[i].CommittedAt}
		if l != w {
			t.Errorf("record %d = %+v, want %+v", i+1, l, w)
		}
	}
	active := ActiveTaskLinks(got)
	if len(active) != 2 || active[0].Number != 2 || active[1].Number != 4 {
		t.Errorf("ActiveTaskLinks = %+v, want records 2 and 4", active)
	}

	h, err := s.TaskHistory("app", "t-1")
	if err != nil || !slices.Equal(h.Links, got) || h.Receipts[len(h.Receipts)-1].Record != TaskLinkRecord(4) {
		t.Fatalf("TaskHistory links = %+v, receipts %+v, %v", h.Links, h.Receipts, err)
	}
}

func TestAddTaskLinkRefusals(t *testing.T) {
	s, spec, _ := linkFixture(t)
	if _, err := s.AddTaskLink("link-1", "app", "t-1", TaskLinkAdd, name(spec), 0, "Read it."); err != nil {
		t.Fatal(err)
	}
	mustCreate(t, s, "m-done", sampleTask("app", "t-done"))
	if _, err := s.SetTaskState("close-done", "app", "t-done", TaskDone); err != nil {
		t.Fatal(err)
	}
	own := DocumentRef{Repository: "app", TaskID: "t-1", Name: "plan-1-spec", Path: "tasks/app/t-1/plan-1/spec.md", SHA256: strings.Repeat("c", 64)}
	if _, err := s.PutDocument("doc-own", own); err != nil {
		t.Fatal(err)
	}
	missing := DocumentRef{Repository: "app", TaskID: "src", Name: "plan-9-spec"}
	for label, tc := range map[string]struct {
		task  string
		op    TaskLinkOp
		src   DocumentRef
		prior int
		text  string
		want  error
	}{
		"unknown task":           {"t-9", TaskLinkAdd, spec, 0, "Read it.", ErrNotFound},
		"done task":              {"t-done", TaskLinkAdd, spec, 0, "Read it.", ErrInvalid},
		"unknown source":         {"t-1", TaskLinkAdd, missing, 0, "Read it.", ErrNotFound},
		"unknown source task":    {"t-1", TaskLinkAdd, DocumentRef{Repository: "app", TaskID: "nope", Name: "plan-5-spec"}, 0, "Read it.", ErrNotFound},
		"self link":              {"t-1", TaskLinkAdd, own, 0, "Read it.", ErrInvalid},
		"already linked":         {"t-1", TaskLinkAdd, spec, 1, "Read it.", ErrExists},
		"stale prior":            {"t-1", TaskLinkRemove, spec, 0, "Drop it.", ErrExists},
		"remove when not linked": {"t-1", TaskLinkRemove, DocumentRef{Repository: "lib", TaskID: "other", Name: "research-1-notes"}, 0, "Drop it.", ErrInvalid},
		"blank text":             {"t-1", TaskLinkRemove, spec, 1, " \n", ErrInvalid},
		"oversized text":         {"t-1", TaskLinkRemove, spec, 1, strings.Repeat("x", MaxTextBytes+1), ErrInvalid},
		"unknown op":             {"t-1", "move", spec, 1, "Move it.", ErrInvalid},
	} {
		rc, err := s.AddTaskLink("bad-"+strings.ReplaceAll(label, " ", "-"), "app", tc.task, tc.op, name(tc.src), tc.prior, tc.text)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: AddTaskLink = %+v, %v, want %v", label, rc, err, tc.want)
		}
	}
	if _, err := s.AddTaskLink("bad-source-named", "app", "t-1", TaskLinkAdd, missing, 0, "Read it."); err == nil || !strings.Contains(err.Error(), "app/src/plan-9-spec") {
		t.Errorf("unknown source error %v does not name app/src/plan-9-spec", err)
	}
	if got, err := s.TaskLinks("app", "t-1"); err != nil || len(got) != 1 {
		t.Errorf("TaskLinks after refusals = %+v, %v", got, err)
	}
	if got, err := s.TaskLinks("app", "t-done"); err != nil || len(got) != 0 {
		t.Errorf("TaskLinks of the done task = %+v, %v", got, err)
	}
	if _, err := s.AddTaskLink("link-1", "app", "t-1", TaskLinkAdd, name(spec), 0, "Other words."); !errors.Is(err, ErrMutationConflict) {
		t.Errorf("reused mutation id: err = %v, want ErrMutationConflict", err)
	}
}

func TestDoctorCountsAndDecodesTaskLinks(t *testing.T) {
	s, spec, notes := linkFixture(t)
	for i, d := range []DocumentRef{spec, notes} {
		if _, err := s.AddTaskLink("link-"+string(rune('1'+i)), "app", "t-1", TaskLinkAdd, name(d), 0, "Read "+d.Name+"."); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	app := slices.IndexFunc(rep.Repositories, func(r RepositoryCounts) bool { return r.Repository == "app" })
	if app < 0 || rep.Repositories[app].TaskLinks != 2 {
		t.Fatalf("doctor repositories = %+v", rep.Repositories)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketTaskLinks).Put([]byte("t-1/9999999999"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "repository app") || rep.Repositories[app].TaskLinks != 3 {
		t.Fatalf("doctor after a bad record = %+v, %v", rep, err)
	}
}
