package cli

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/web"
)

// TestWebCloseDecisionStalePage: a close decision is bound to its proposed delivery record, so an answer from a page
// loaded on another proposal is refused, and one on the proposal it shows is recorded with its option.
func TestWebCloseDecisionStalePage(t *testing.T) {
	w, _ := planAnswerer(t)
	subject := "sha256:" + strings.Repeat("c", 64)
	d := store.Decision{Repository: "app", TaskID: "a-approve", ID: "close-build-1", Kind: store.DecisionClose,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: subject}, Options: []string{"close", "retry"},
		Question: "Close a-approve as delivered by #1?"}
	if _, err := withStore(w.c.store, false, func(s *store.Store) (store.Receipt, error) { return s.OpenDecision("open:close-build-1", d) }); err != nil {
		t.Fatal(err)
	}
	x := web.Answer{Repository: "app", TaskID: "a-approve", Decision: "close-build-1", Option: "close", Login: pageLogin,
		Subject: "sha256:" + strings.Repeat("d", 64)}
	refusedWith(t, w.Answer(x), "is about another version now")
	x.Subject = subject
	if err := w.Answer(x); err != nil {
		t.Fatalf("answer on the current proposal = %v", err)
	}
	got, err := withStore(w.c.store, true, func(s *store.Store) ([]store.Decision, error) { return s.Decisions("app", "a-approve") })
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range got {
		if g.ID == "close-build-1" && (!g.Answered || g.Choice != "close") {
			t.Errorf("close decision = %+v, want it answered close", g)
		}
	}
}
