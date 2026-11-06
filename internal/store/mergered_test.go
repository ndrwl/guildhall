package store

import (
	"errors"
	"slices"
	"testing"
)

// TestMergeBatchVerdicts: members record their link's verdict only while the batch is red, at their own link, once;
// a member leaving a red batch, as its culprit does, takes the members after it out and keeps the batch red; the
// green rest moves back to testing; and a changed link clears the member's verdict and sends the batch assembling.
func TestMergeBatchVerdicts(t *testing.T) {
	s := trainBatch(t)
	for name, err := range map[string]error{
		"not red":      second(s.SetMergeBatchVerdict("v-early", "app", "b", linkB, VerdictPassed)),
		"bad verdict":  second(s.SetMergeBatchVerdict("v-bad", "app", "b", linkB, "green")),
		"not its link": second(s.SetMergeBatchVerdict("v-link", "app", "b", linkC, VerdictPassed)),
		"not a member": second(s.SetMergeBatchVerdict("v-d", "app", "d", linkB, VerdictPassed)),
		"short link":   second(s.SetMergeBatchVerdict("v-short", "app", "b", "abc", VerdictPassed)),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := s.SetMergeBatchState("red", "app", "a", BatchRed, ""); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct{ id, link, verdict string }{{"a", linkA, VerdictPassed}, {"b", linkB, VerdictFailed}, {"c", linkC, VerdictFailed}} {
		if _, err := s.SetMergeBatchVerdict("v-"+v.id, "app", v.id, v.link, v.verdict); err != nil {
			t.Fatal(err)
		}
	}
	if err := second(s.SetMergeBatchVerdict("v-b-again", "app", "b", linkB, VerdictPassed)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed verdict = %v, want ErrInvalid", err)
	}
	if _, err := s.SetMergeBatchVerdict("v-b-same", "app", "b", linkB, VerdictFailed); err != nil {
		t.Fatalf("same verdict again = %v", err)
	}
	// b, the culprit, goes back to build: c leaves the batch with it and keeps its place in the queue.
	if _, err := s.LeaveMergeQueue("b-escalated", "app", "b", true); err != nil {
		t.Fatal(err)
	}
	b := standingTrain(t, s)
	if b.State != BatchRed || len(b.Members) != 1 || b.Members[0].Verdict != VerdictPassed || b.Members[0].Link != linkA {
		t.Fatalf("batch after its culprit left = %+v", b)
	}
	if q, _ := s.MergeQueue("app"); !slices.ContainsFunc(q.Entries, func(e QueuedEntry) bool { return e.TaskID == "c" }) {
		t.Fatalf("c left the queue: %+v", q.Entries)
	}
	if _, err := s.SetMergeBatchState("retest", "app", "a", BatchTesting, ""); err != nil {
		t.Fatalf("red to testing = %v", err)
	}
	if _, err := s.SetMergeBatchState("red-2", "app", "a", BatchRed, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMergeBatchLink("link-a-2", "app", "a", 1, linkC); err != nil {
		t.Fatal(err)
	}
	if b := standingTrain(t, s); b.State != BatchAssembling || b.Members[0].Verdict != "" {
		t.Fatalf("batch after a new leader link = %+v", b)
	}
}
