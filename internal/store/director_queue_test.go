package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/digest"
)

const sampleLesson = "# Rebase before review\n\nKeep branches current.\n\n## Evidence\n\nd12 failed twice.\n\n## Limits\n\nOnly for long-lived tasks.\n"

func sampleIncident(id string) IncidentReport {
	return IncidentReport{
		Schema: "guildhall.incident/1", ID: id,
		Repository: "app", Task: "t-1", Step: "build", Attempt: "build-1",
		Release: "v1.2.3", Workflow: "sha256:" + strings.Repeat("a", 64),
		ObservedState: "attempt stuck in starting", ObservedAt: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC),
		Error:    "artisan start: owner live",
		Evidence: []Evidence{{Ref: "state/logs/build-1.log", Digest: "sha256:" + strings.Repeat("b", 64)}},
		Tried:    "artisan stop", Done: "step retry", Workaround: "retry", Risk: "may recur",
	}
}

// TestLessons: a lesson is filed proposed with the copy's origin, its title and digest; filing it again replays its
// receipt, another body under the id conflicts, and resolve records the local decision once.
func TestLessons(t *testing.T) {
	s, _ := newStore(t)
	if _, err := s.QueueOrigin(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("origin of a new store: %v, want ErrNotFound", err)
	}
	id := DefaultLessonID(sampleLesson)
	l, r, err := s.ProposeLesson(Lesson{ID: id, Body: sampleLesson})
	if err != nil || l.State != LessonProposed || l.Title != "Rebase before review" || l.Digest != string(digest.Of([]byte(sampleLesson))) ||
		len(l.Origin) != 16 || l.Receipt != r.Sequence || r.MutationID != "lesson:"+id || !strings.HasPrefix(id, "l-") {
		t.Fatalf("lesson %+v, receipt %+v, %v", l, r, err)
	}
	if o, err := s.QueueOrigin(); err != nil || o != l.Origin {
		t.Fatalf("origin %q, %v; want %q", o, err, l.Origin)
	}
	again, r2, err := s.ProposeLesson(Lesson{ID: id, Body: sampleLesson})
	if err != nil || r2 != r || again != l {
		t.Fatalf("replay %+v, %+v, %v", again, r2, err)
	}
	if _, _, err := s.ProposeLesson(Lesson{ID: id, Body: sampleLesson + "more\n"}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("other body under the id: %v, want ErrMutationConflict", err)
	}
	second, _, err := s.ProposeLesson(Lesson{ID: "keep-notes-short", Body: strings.Replace(sampleLesson, "Rebase", "Trim", 1)})
	if err != nil || second.Origin != l.Origin {
		t.Fatalf("second lesson %+v, %v", second, err)
	}

	got, rr, err := s.ResolveLesson(id, LessonAdopted)
	if err != nil || got.State != LessonAdopted || got.Resolved == nil || got.Resolved.Receipt != rr.Sequence {
		t.Fatalf("resolve %+v, %+v, %v", got, rr, err)
	}
	if again, rr2, err := s.ResolveLesson(id, LessonAdopted); err != nil || rr2 != rr || again.State != LessonAdopted {
		t.Fatalf("resolve again %+v, %+v, %v", again, rr2, err)
	}
	if _, _, err := s.ResolveLesson(id, LessonDeclined); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolve the other way: %v, want ErrInvalid", err)
	}
	if _, _, err := s.ResolveLesson("nope", LessonDeclined); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve unknown: %v, want ErrNotFound", err)
	}
	all, err := s.Lessons()
	if err != nil || len(all) != 2 || all[0].ID != id || all[1].ID != "keep-notes-short" {
		t.Fatalf("lessons %+v, %v", all, err)
	}
}

func TestLessonRefusals(t *testing.T) {
	s, _ := newStore(t)
	big := sampleLesson + strings.Repeat("x", MaxQueueItemBytes)
	for name, l := range map[string]Lesson{
		"no title":        {ID: "a", Body: "Rebase\n## Evidence\n## Limits\n"},
		"no evidence":     {ID: "a", Body: "# T\n## Limits\n"},
		"no limits":       {ID: "a", Body: "# T\n## Evidence\n"},
		"fenced sections": {ID: "a", Body: "# T\n\n```markdown\n## Evidence\n## Limits\n```\n"},
		"tilde fence":     {ID: "a", Body: "# T\n## Evidence\n~~~~\n## Limits\n~~~\nstill fenced\n"},
		"bad id":          {ID: "A b", Body: sampleLesson},
		"long id":         {ID: "a" + strings.Repeat("b", MaxQueueIDBytes), Body: sampleLesson},
		"over the cap":    {ID: "a", Body: big},
		"store-set time":  {ID: "a", Body: sampleLesson, Time: time.Now()},
		"empty":           {ID: "a", Body: ""},
	} {
		_, _, err := s.ProposeLesson(l)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
		if name == "over the cap" && (err == nil || !strings.Contains(err.Error(), "is 65"+"6")) {
			t.Errorf("over the cap names no size: %v", err)
		}
	}
	if rep, err := s.Doctor(); err != nil || rep.Lessons != 0 || rep.Receipts != 0 {
		t.Fatalf("refusals recorded something: %+v, %v", rep, err)
	}
}

// TestIncidents: an incident is filed open, replays, conflicts under another report, takes observations without
// changing its report, and resolves along its allowed moves.
func TestIncidents(t *testing.T) {
	s, _ := newStore(t)
	rep := sampleIncident("stuck-owner")
	inc, r, err := s.FileIncident(rep)
	if err != nil || inc.State != IncidentOpen || inc.Report.Error != rep.Error || len(inc.Origin) != 16 || inc.Receipt != r.Sequence || r.MutationID != "incident:stuck-owner" {
		t.Fatalf("incident %+v, %+v, %v", inc, r, err)
	}
	if _, r2, err := s.FileIncident(rep); err != nil || r2 != r {
		t.Fatalf("replay %+v, %v", r2, err)
	}
	other := rep
	other.Error = "something else"
	if _, _, err := s.FileIncident(other); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("other report under the id: %v, want ErrMutationConflict", err)
	}

	obs := ObservationReport{Schema: "guildhall.incident-observation/1", ObservedAt: time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), Note: "happened again"}
	o1, or1, err := s.ObserveIncident("stuck-owner", obs)
	if err != nil || o1.Sequence != 1 || o1.Incident != "stuck-owner" || o1.Receipt != or1.Sequence {
		t.Fatalf("observation %+v, %v", o1, err)
	}
	if again, or2, err := s.ObserveIncident("stuck-owner", obs); err != nil || or2 != or1 || again.Sequence != 1 || again.Receipt != o1.Receipt {
		t.Fatalf("observation replay %+v, %+v, %v", again, or2, err)
	}
	obs.Note = "and again"
	if o2, _, err := s.ObserveIncident("stuck-owner", obs); err != nil || o2.Sequence != 2 {
		t.Fatalf("second observation %+v, %v", o2, err)
	}
	if _, _, err := s.ObserveIncident("nope", obs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("observe unknown: %v, want ErrNotFound", err)
	}
	got, err := s.Incident("stuck-owner")
	if err != nil || len(got.Observations) != 2 || got.Report.Error != rep.Error || got.Digest != inc.Digest {
		t.Fatalf("incident after observations %+v, %v", got, err)
	}

	if d, _, err := s.ResolveIncident("stuck-owner", IncidentDeferred); err != nil || d.State != IncidentDeferred {
		t.Fatalf("defer %+v, %v", d, err)
	}
	if a, _, err := s.ResolveIncident("stuck-owner", IncidentAccepted); err != nil || a.State != IncidentAccepted {
		t.Fatalf("accept after defer %+v, %v", a, err)
	}
	if _, _, err := s.ResolveIncident("stuck-owner", IncidentDeferred); !errors.Is(err, ErrInvalid) {
		t.Fatalf("defer after accept: %v, want ErrInvalid", err)
	}
	if _, _, err := s.ResolveIncident("stuck-owner", "open"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("resolve to open: %v, want ErrInvalid", err)
	}
	all, err := s.Incidents()
	if err != nil || len(all) != 1 || len(all[0].Observations) != 2 {
		t.Fatalf("incidents %+v, %v", all, err)
	}
}

func TestIncidentRefusals(t *testing.T) {
	s, _ := newStore(t)
	for name, mod := range map[string]func(*IncidentReport){
		"schema":            func(r *IncidentReport) { r.Schema = "guildhall.incident/2" },
		"no id":             func(r *IncidentReport) { r.ID = "" },
		"no task":           func(r *IncidentReport) { r.Task = "" },
		"two-line step":     func(r *IncidentReport) { r.Step = "a\nb" },
		"no observed time":  func(r *IncidentReport) { r.ObservedAt = time.Time{} },
		"no error":          func(r *IncidentReport) { r.Error = " " },
		"no risk":           func(r *IncidentReport) { r.Risk = "" },
		"bad evidence hash": func(r *IncidentReport) { r.Evidence[0].Digest = "sha256:ABC" },
		"over the cap":      func(r *IncidentReport) { r.Tried = strings.Repeat("x", MaxQueueItemBytes) },
	} {
		r := sampleIncident("x")
		r.Evidence = append([]Evidence(nil), r.Evidence...)
		mod(&r)
		if _, _, err := s.FileIncident(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
	na := sampleIncident("pre-task")
	na.Repository, na.Task, na.Step, na.Attempt = IncidentNotApplicable, IncidentNotApplicable, IncidentNotApplicable, IncidentNotApplicable
	if _, _, err := s.FileIncident(na); err != nil {
		t.Fatalf("pre-task incident: %v", err)
	}
	if _, _, err := s.ObserveIncident("pre-task", ObservationReport{Schema: "guildhall.incident-observation/1", Note: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("observation without time: %v, want ErrInvalid", err)
	}
}

// TestMarkCollected: a collection mark with the item's digest moves it to collected, after which resolve is refused;
// a mismatched digest leaves the item pending.
func TestMarkCollected(t *testing.T) {
	s, _ := newStore(t)
	l, _, err := s.ProposeLesson(Lesson{ID: "l-1", Body: sampleLesson})
	if err != nil {
		t.Fatal(err)
	}
	inc, _, err := s.FileIncident(sampleIncident("i-1"))
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	if _, err := s.MarkCollected(QueueLesson, "l-1", "sha256:"+strings.Repeat("0", 64), commit); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mismatched digest: %v, want ErrDigestMismatch", err)
	}
	if got, _ := s.Lesson("l-1"); got.State != LessonProposed || got.Collected != nil {
		t.Fatalf("a mismatch changed the lesson: %+v", got)
	}
	r, err := s.MarkCollected(QueueLesson, "l-1", l.Digest, commit)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.MarkCollected(QueueLesson, "l-1", l.Digest, commit); err != nil || again != r {
		t.Fatalf("mark again %+v, %v", again, err)
	}
	if _, err := s.MarkCollected(QueueIncident, "i-1", inc.Digest, "nothex"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad commit: %v, want ErrInvalid", err)
	}
	if _, err := s.MarkCollected(QueueIncident, "i-1", inc.Digest, commit); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Lesson("l-1"); got.State != LessonCollected || got.Collected == nil || got.Collected.Commit != commit {
		t.Fatalf("collected lesson %+v", got)
	}
	if _, _, err := s.ResolveLesson("l-1", LessonAdopted); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "collected") {
		t.Fatalf("resolve a collected lesson: %v, want ErrInvalid", err)
	}
	if _, _, err := s.ResolveIncident("i-1", IncidentAccepted); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "collected") {
		t.Fatalf("resolve a collected incident: %v, want ErrInvalid", err)
	}
}

// TestQueueDoctorAndExport: store doctor counts lessons, incidents and observations, and an export imports into a
// store that exports the same and still lists them.
func TestQueueDoctorAndExport(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.ProposeLesson(Lesson{ID: "l-1", Body: sampleLesson}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FileIncident(sampleIncident("i-1")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ObserveIncident("i-1", ObservationReport{Schema: "guildhall.incident-observation/1", ObservedAt: time.Now().UTC(), Note: "again"}); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 || rep.Lessons != 1 || rep.Incidents != 1 || rep.IncidentObservations != 1 {
		t.Fatalf("doctor %+v, %v", rep, err)
	}
	first, _ := export(t, s, ExportOptions{})
	restored := filepath.Join(t.TempDir(), "restored.store")
	if rep, err := Import(restored, first); err != nil || len(rep.Problems) != 0 || rep.Lessons != 1 || rep.Incidents != 1 {
		t.Fatalf("import %+v, %v", rep, err)
	}
	r := openTest(t, restored)
	second, _ := export(t, r, ExportOptions{})
	sameTree(t, "second export", readTree(t, second), readTree(t, first))
	origin, _ := s.QueueOrigin()
	if o, err := r.QueueOrigin(); err != nil || o != origin {
		t.Fatalf("restored origin %q, %v; want %q", o, err, origin)
	}
	if inc, err := r.Incident("i-1"); err != nil || len(inc.Observations) != 1 {
		t.Fatalf("restored incident %+v, %v", inc, err)
	}
}

// TestLessonFencedSections: "## Evidence" and "## Limits" inside a fenced code block are quoted, not sections, and the
// sections after a closed fence still count.
func TestLessonFencedSections(t *testing.T) {
	body := "# Quote the template\n\n```markdown\n# Other\n## Evidence\n```\n\n## Evidence\n\nd12.\n\n## Limits\n\nNone.\n"
	if title, err := LessonTitle(body); err != nil || title != "Quote the template" {
		t.Fatalf("LessonTitle = %q, %v", title, err)
	}
}

// TestIncidentSizeBound: an observation that would take an incident and its observations past MaxIncidentBytes is
// refused with the size, the incident keeps what it had, and the incident read with its observations stays near the
// bound, so it can never fill a pending list on its own.
func TestIncidentSizeBound(t *testing.T) {
	s, _ := newStore(t)
	if _, _, err := s.FileIncident(sampleIncident("big")); err != nil {
		t.Fatal(err)
	}
	note := strings.Repeat("n", MaxQueueItemBytes-200)
	var n int
	var err error
	for n = 1; n < 100; n++ {
		obs := ObservationReport{Schema: "guildhall.incident-observation/1", ObservedAt: time.Date(2026, 11, 3, 10, n, 0, 0, time.UTC), Note: note}
		if _, _, err = s.ObserveIncident("big", obs); err != nil {
			break
		}
	}
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "the limit is 524288") {
		t.Fatalf("observation %d: %v, want ErrInvalid naming the limit", n, err)
	}
	inc, err := s.Incident("big")
	if err != nil || n < 2 || len(inc.Observations) != n-1 {
		t.Fatalf("incident after the refusal: %d observations, %v; %d accepted", len(inc.Observations), err, n-1)
	}
	if b, _ := json.Marshal(inc); len(b) > MaxIncidentBytes+1024 {
		t.Fatalf("incident with its observations is %d bytes as JSON", len(b))
	}
	if _, _, err := s.FileIncident(sampleIncident("other")); err != nil {
		t.Fatal(err)
	}
	small := ObservationReport{Schema: "guildhall.incident-observation/1", ObservedAt: time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC), Note: "x"}
	if _, _, err := s.ObserveIncident("other", small); err != nil {
		t.Fatalf("another incident's observation: %v", err)
	}
}
