package store

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// recommending is a Director recommendation of option on decision tag.
func recommending(tag, option, words string) Saying {
	return Saying{Author: ConversationDirector, Tag: tag, Kind: ConversationRecommendation, Text: "I recommend " + option + ".", Option: option, Words: words}
}

// mustRecommendation reads decision id's recommendation.
func mustRecommendation(t *testing.T, s *Store, id string) Recommendation {
	t.Helper()
	r, err := s.DecisionRecommendation("app", "t-1", id)
	if err != nil {
		t.Fatalf("DecisionRecommendation(%s) = %v", id, err)
	}
	return r
}

// TestRecommendationLastWriteWins: each writer replaces the decision's one record and bumps its revision; a say
// records its entry's number, the others 0; a replay writes nothing new; TaskHistory carries the record.
func TestRecommendationLastWriteWins(t *testing.T) {
	s := holdFixture(t)
	if _, err := s.DecisionRecommendation("app", "t-1", "q-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no record yet = %v, want ErrNotFound", err)
	}
	digest := sampleDigest('c')
	if _, err := s.ExplainDecision("explain.0", "app", "t-1", "q-1", "Words.", digest, "approve", 0); err != nil {
		t.Fatal(err)
	}
	r := mustRecommendation(t, s, "q-1")
	if r.Option != "approve" || r.Number != 0 || r.Revision != 1 || r.SubjectDigest != digest || r.At.IsZero() {
		t.Fatalf("after the explanation = %+v", r)
	}
	rc, err := s.SayRecommendation("say.1", "app", "t-1", recommending("q-1", "reject", "no, reject"), digest)
	if err != nil || rc.Operation != "say-recommendation" || rc.Record != "conversation/1" {
		t.Fatalf("SayRecommendation = %+v, %v", rc, err)
	}
	if replay, err := s.SayRecommendation("say.1", "app", "t-1", recommending("q-1", "reject", "no, reject"), digest); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.SayRecommendation("say.1", "app", "t-1", recommending("q-1", "reject", "no, reject"), sampleDigest('d')); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another digest under the same id = %v, want ErrMutationConflict", err)
	}
	r = mustRecommendation(t, s, "q-1")
	if r.Option != "reject" || r.Words != "no, reject" || r.Number != 1 || r.Revision != 2 || !r.At.Equal(rc.CommittedAt) {
		t.Fatalf("after the say = %+v", r)
	}
	// An explanation without a recommendation leaves the record as it is.
	if _, err := s.ExplainDecision("explain.1", "app", "t-1", "q-1", "Other words.", digest, "", 1); err != nil {
		t.Fatal(err)
	}
	if got := mustRecommendation(t, s, "q-1"); got != r {
		t.Fatalf("an explanation with no recommendation changed the record: %+v", got)
	}
	if _, err := s.ExplainDecision("explain.2", "app", "t-1", "q-1", "Approve after all.", digest, "approve", 2); err != nil {
		t.Fatal(err)
	}
	if r = mustRecommendation(t, s, "q-1"); r.Option != "approve" || r.Number != 0 || r.Words != "" || r.Revision != 3 {
		t.Fatalf("after explaining again = %+v", r)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.Recommendations) != 1 || h.Recommendations[0] != r {
		t.Fatalf("TaskHistory.Recommendations = %+v, %v", h.Recommendations, err)
	}
	if talk, _ := s.Conversation("app", "t-1"); len(talk) != 1 || talk[0].Saying != recommending("q-1", "reject", "no, reject") {
		t.Errorf("conversation = %+v, want the one recommendation entry", talk)
	}
}

// TestOpenDecisionRecommendation: a decision opened with a recommendation commits it with the decision and its
// entry; a refused recommendation writes neither; Decision.Recommended stays unset.
func TestOpenDecisionRecommendation(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "m-1", sampleTask("app", "t-1"))
	q := Decision{Repository: "app", TaskID: "t-1", ID: "q1", Kind: DecisionQuestion, Question: "Back off or stop?", Options: []string{"back-off", "stop"}}
	asked := say(ConversationDirector, "q1", ConversationSay, q.Question)
	if _, err := s.OpenDecisionSaying("open-q1", q, &asked, &Recommendation{Option: "halt", SubjectDigest: SubjectDigestOf(q)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an option not offered = %v, want ErrInvalid", err)
	}
	if all, _ := s.Decisions("app", "t-1"); len(all) != 0 {
		t.Fatalf("a refused recommendation wrote the decision: %+v", all)
	}
	if talk, _ := s.Conversation("app", "t-1"); len(talk) != 0 {
		t.Fatalf("a refused recommendation wrote the entry: %+v", talk)
	}
	rec := &Recommendation{Option: "stop", SubjectDigest: SubjectDigestOf(q)}
	rc, err := s.OpenDecisionSaying("open-q1", q, &asked, rec)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.OpenDecisionSaying("open-q1", q, &asked, rec); err != nil || replay != rc {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.OpenDecisionSaying("open-q1", q, &asked, &Recommendation{Option: "back-off", SubjectDigest: SubjectDigestOf(q)}); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("another recommendation under the same id = %v, want ErrMutationConflict", err)
	}
	r, err := s.DecisionRecommendation("app", "t-1", "q1")
	if err != nil || r.Option != "stop" || r.Number != 0 || r.Revision != 1 || !r.At.Equal(rc.CommittedAt) {
		t.Fatalf("recommendation = %+v, %v", r, err)
	}
	if d, _ := s.Decisions("app", "t-1"); len(d) != 1 || d[0].Recommended != "" || !r.Current(d[0]) {
		t.Fatalf("decisions = %+v", d)
	}
}

// TestRecommendationRefusals: each writer's refusals in its transaction write nothing: no record, no entry and no
// explanation.
func TestRecommendationRefusals(t *testing.T) {
	s := holdFixture(t)
	mustCreate(t, s, "m-g", sampleTask("app", "t-9"))
	if _, err := s.OpenDecision("open-g", Decision{Repository: "app", TaskID: "t-1", ID: "g-1", Kind: DecisionQuestion, Question: "Anything?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VoidDecision("void-q-1", "app", "t-1", "q-1", "superseded"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("open-q-3", sampleDecision("q-3")); err != nil {
		t.Fatal(err)
	}
	digest := sampleDigest('c')
	for name, c := range map[string]struct {
		say    Saying
		digest string
		want   error
	}{
		"answered":       {recommending("q-2", "approve", ""), digest, ErrInvalid},
		"void":           {recommending("q-1", "approve", ""), digest, ErrInvalid},
		"no options":     {recommending("g-1", "yes", ""), digest, ErrInvalid},
		"not offered":    {recommending("q-3", "merge", ""), digest, ErrInvalid},
		"no decision":    {recommending("q-9", "approve", ""), digest, ErrInvalid},
		"blank digest":   {recommending("q-3", "approve", ""), " ", ErrInvalid},
		"spaced digest":  {recommending("q-3", "approve", ""), "sha256: x", ErrInvalid},
		"long digest":    {recommending("q-3", "approve", ""), strings.Repeat("a", MaxSubjectDigestBytes+1), ErrInvalid},
		"long words":     {recommending("q-3", "approve", strings.Repeat("w", MaxTextBytes+1)), digest, ErrInvalid},
		"not a say":      {say(ConversationDirector, "q-3", ConversationSay, "hi"), digest, ErrInvalid},
		"guildmaster's":  {Saying{Author: ConversationGuildmasterChat, Tag: "q-3", Kind: ConversationRecommendation, Text: "x", Option: "approve"}, digest, ErrInvalid},
		"no option word": {Saying{Author: ConversationDirector, Tag: "q-3", Kind: ConversationRecommendation, Text: "x"}, digest, ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.SayRecommendation("say-"+name, "app", "t-1", c.say, c.digest); !errors.Is(err, c.want) {
				t.Errorf("SayRecommendation = %v, want %v", err, c.want)
			}
		})
	}
	if _, err := s.ExplainDecision("explain-q-3", "app", "t-1", "q-3", "Words.", SubjectDigestOf(sampleDecision("q-3")), "merge", 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("an explanation recommending an option not offered = %v", err)
	}
	if _, err := s.SetTaskState("close-t-9", "app", "t-9", TaskDone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SayRecommendation("say-closed", "app", "t-9", recommending("q-3", "approve", ""), digest); !errors.Is(err, ErrInvalid) {
		t.Errorf("a closed task = %v, want ErrInvalid", err)
	}
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || len(h.Recommendations) != 0 || len(h.Conversation) != 0 || len(h.Explanations) != 0 {
		t.Fatalf("refusals wrote: %+v, %+v, %+v, %v", h.Recommendations, h.Conversation, h.Explanations, err)
	}
}

// TestRecommendationCurrent: a recommendation is current while its digest is the decision's subject digest, and a
// say made against another digest is recorded, stale.
func TestRecommendationCurrent(t *testing.T) {
	s := holdFixture(t)
	if _, err := s.SayRecommendation("say.old", "app", "t-1", recommending("q-1", "approve", ""), sampleDigest('b')); err != nil {
		t.Fatalf("a stale digest is recorded, not refused: %v", err)
	}
	d := sampleDecision("q-1")
	r := mustRecommendation(t, s, "q-1")
	if r.Current(d) {
		t.Errorf("a recommendation against an older digest is current: %+v", r)
	}
	if r.SubjectDigest = sampleDigest('c'); !r.Current(d) {
		t.Errorf("a recommendation against the subject is stale")
	}
	q := Decision{Kind: DecisionQuestion, Question: "Keep it?"}
	if !(Recommendation{SubjectDigest: SubjectDigestOf(q)}).Current(q) {
		t.Errorf("a question's recommendation against its text is stale")
	}
}

// TestDoctorCountsRecommendations: a store with no bucket reads no record; doctor counts the bucket and reports one
// it cannot decode.
func TestDoctorCountsRecommendations(t *testing.T) {
	s := holdFixture(t)
	h, err := s.TaskHistory("app", "t-1")
	if err != nil || h.Recommendations == nil || len(h.Recommendations) != 0 {
		t.Fatalf("a store with no bucket: %+v, %v", h.Recommendations, err)
	}
	if _, err := s.SayRecommendation("say.1", "app", "t-1", recommending("q-1", "approve", ""), sampleDigest('c')); err != nil {
		t.Fatal(err)
	}
	rep, err := s.Doctor()
	app := slices.IndexFunc(rep.Repositories, func(r RepositoryCounts) bool { return r.Repository == "app" })
	if err != nil || len(rep.Problems) != 0 || app < 0 || rep.Repositories[app].DecisionRecommendations != 1 {
		t.Fatalf("doctor = %+v, %v", rep, err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return repoBucket(tx, "app", bucketDecisionRecommendations).Put([]byte("t-1/q-9"), []byte("{not json"))
	}); err != nil {
		t.Fatal(err)
	}
	if rep, err = s.Doctor(); err != nil || len(rep.Problems) != 1 || rep.Repositories[app].DecisionRecommendations != 2 {
		t.Fatalf("doctor after a bad record = %+v, %v", rep, err)
	}
}

// TestOldRecommendedDecodes: a question written with recommended, before the record, still decodes with it.
func TestOldRecommendedDecodes(t *testing.T) {
	var d Decision
	if err := json.Unmarshal([]byte(`{"repository":"app","task_id":"t-1","id":"q1","kind":"question","question":"Stop?","options":["back-off","stop"],"recommended":"stop"}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Recommended != "stop" || validateDecision(d) != nil {
		t.Errorf("old decision = %+v, %v", d, validateDecision(d))
	}
}
