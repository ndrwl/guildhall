package store

import (
	"errors"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// TestExplainDecision: an explanation replays under its id, is refused at a stale revision, and explaining again
// replaces the text under the next revision's id; the decision itself does not change, and TaskHistory and
// DirectorView read the record.
func TestExplainDecision(t *testing.T) {
	s := holdFixture(t)
	digest := sampleDigest('c')
	rc, err := s.ExplainDecision("explain.0", "app", "t-1", "q-1", "The plan **builds** the page.", digest, "approve", 0)
	if err != nil || rc.Operation != "explain-decision" || rc.Record != "decision-explain/q-1" || rc.TaskID != "t-1" || rc.CommittedAt.IsZero() {
		t.Fatalf("ExplainDecision = %+v, %v", rc, err)
	}
	if replay, err := s.ExplainDecision("explain.0", "app", "t-1", "q-1", "The plan **builds** the page.", digest, "approve", 0); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, rc)
	}
	if _, err := s.ExplainDecision("explain.0", "app", "t-1", "q-1", "Other words.", digest, "", 0); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("other words under the same id = %v, want ErrMutationConflict", err)
	}
	if _, err := s.ExplainDecision("explain.stale", "app", "t-1", "q-1", "Other words.", digest, "", 0); !errors.Is(err, ErrExists) {
		t.Fatalf("explanation at a stale revision = %v, want ErrExists", err)
	}
	got, err := s.DecisionExplanation("app", "t-1", "q-1")
	if err != nil || got.Text != "The plan **builds** the page." || got.SubjectDigest != digest || got.Recommend != "approve" || got.Revision != 1 ||
		!got.At.Equal(rc.CommittedAt) {
		t.Fatalf("DecisionExplanation = %+v, %v", got, err)
	}
	if _, err := s.ExplainDecision("explain.1", "app", "t-1", "q-1", "Second words.", digest, "", 1); err != nil {
		t.Fatalf("explaining again = %v", err)
	}
	if got, err = s.DecisionExplanation("app", "t-1", "q-1"); err != nil || got.Text != "Second words." || got.Recommend != "" || got.Revision != 2 {
		t.Fatalf("after explaining again = %+v, %v", got, err)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.Explanations) != 1 || h.Explanations[0].Revision != 2 {
		t.Fatalf("TaskHistory.Explanations = %+v, %v", h.Explanations, err)
	}
	if i := slices.IndexFunc(h.Decisions, func(d Decision) bool { return d.ID == "q-1" }); i < 0 || !h.Decisions[i].Open() {
		t.Errorf("q-1 is no longer open: %+v", h.Decisions)
	}
	v, err := s.DirectorView()
	if err != nil || len(v.Explanations) != 1 || v.Explanations[0].Decision != "q-1" {
		t.Fatalf("DirectorView.Explanations = %+v, %v", v.Explanations, err)
	}
	if _, err := s.DecisionExplanation("app", "t-1", "q-2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("explanation of an unexplained decision = %v, want ErrNotFound", err)
	}
}

// TestExplainDecisionRefusals: an answered, void or unknown decision, blank, oversized or non-UTF-8 text, a blank
// digest and a recommendation that is not one of the decision's options are refused, writing nothing.
func TestExplainDecisionRefusals(t *testing.T) {
	s := holdFixture(t)
	q := Decision{Repository: "app", TaskID: "t-1", ID: "q-3", Kind: DecisionQuestion, Question: "Which?"}
	if _, err := s.OpenDecision("open-q-3", q); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("open-q-4", sampleDecision("q-4")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VoidDecision("void-q-4", "app", "t-1", "q-4", "the spec changed"); err != nil {
		t.Fatal(err)
	}
	digest := sampleDigest('c')
	for name, tc := range map[string]struct {
		id, text, digest, recommend string
		want                        error
	}{
		"answered":               {"q-2", "Words.", digest, "", ErrInvalid},
		"void":                   {"q-4", "Words.", digest, "", ErrInvalid},
		"unknown":                {"q-9", "Words.", digest, "", ErrNotFound},
		"blank":                  {"q-1", " \n", digest, "", ErrInvalid},
		"oversized":              {"q-1", strings.Repeat("a", MaxTextBytes+1), digest, "", ErrInvalid},
		"not UTF-8":              {"q-1", "\xff", digest, "", ErrInvalid},
		"blank digest":           {"q-1", "Words.", "", "", ErrInvalid},
		"digest with a space":    {"q-1", "Words.", "sha256: ab", "", ErrInvalid},
		"unknown recommendation": {"q-1", "Words.", digest, "approved", ErrInvalid},
		"no options to name":     {"q-3", "Words.", SubjectDigestOf(q), "yes", ErrInvalid},
	} {
		if _, err := s.ExplainDecision("explain-"+strings.ReplaceAll(name, " ", "-"), "app", "t-1", tc.id, tc.text, tc.digest, tc.recommend, 0); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	v, err := s.DirectorView()
	if err != nil || len(v.Explanations) != 0 {
		t.Fatalf("refusals wrote %+v, %v", v.Explanations, err)
	}
}

// TestSubjectDigestOf: a decision's explanation is written against its subject's digest, or, with no subject, its
// question's, and Current says whether it still matches.
func TestSubjectDigestOf(t *testing.T) {
	d := sampleDecision("q-1")
	if got := SubjectDigestOf(d); got != d.Subject.Digest {
		t.Errorf("SubjectDigestOf(subject) = %q", got)
	}
	q := Decision{Kind: DecisionQuestion, Question: "Which?"}
	got := SubjectDigestOf(q)
	if !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 || got == SubjectDigestOf(Decision{Question: "Which one?"}) {
		t.Errorf("SubjectDigestOf(question) = %q", got)
	}
	if e := (DecisionExplanation{SubjectDigest: got}); !e.Current(q) || e.Current(d) {
		t.Errorf("Current = %v, %v", e.Current(q), e.Current(d))
	}
}

// TestDoctorCountsAndDecodesExplanations: doctor counts the explanations and reports one it cannot decode.
func TestDoctorCountsAndDecodesExplanations(t *testing.T) {
	s := holdFixture(t)
	if _, err := s.ExplainDecision("explain.0", "app", "t-1", "q-1", "Words.", sampleDigest('c'), "", 0); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	if err != nil || len(rep.Problems) != 0 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	app := slices.IndexFunc(rep.Repositories, func(r RepositoryCounts) bool { return r.Repository == "app" })
	if app < 0 || rep.Repositories[app].DecisionExplanations != 1 {
		t.Fatalf("doctor repositories = %+v", rep.Repositories)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketDecisionExplanations).Put([]byte("t-1/q-9"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	rep, err = s.Doctor()
	if err != nil || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "repository app") || rep.Repositories[app].DecisionExplanations != 2 {
		t.Fatalf("doctor after a bad record = %+v, %v", rep, err)
	}
}
