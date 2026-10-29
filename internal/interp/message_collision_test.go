package interp_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

// sendEarlier puts a Director message under id with text, as an older binary sent it before its template changed.
func sendEarlier(t *testing.T, e *env, id, typ, text string) {
	t.Helper()
	m := messaging.Envelope{Schema: "guildhall.message/1", ID: id, Recipient: store.RecipientDirector, Repository: "app", Task: "t7", Type: typ, Text: text}
	if _, err := e.mailbox.Send(store.Recipient{Role: store.RecipientDirector}, m); err != nil {
		t.Fatal(err)
	}
}

// countID reports how many of the Director's messages have id.
func countID(t *testing.T, e *env, id string) int {
	t.Helper()
	n := 0
	for _, got := range e.inbox(t) {
		if got == id {
			n++
		}
	}
	return n
}

// A recorded pr-blocked observation whose report an older binary sent with other text is already reported: the
// replay on every advance, a recheck with task start and a send-back all go on, and the earlier message stands.
func TestReplayedReportWithChangedTextIsAlreadyReported(t *testing.T) {
	for _, b := range backends {
		for _, sendBack := range []bool{false, true} {
			name := b.name + "/task-start"
			if sendBack {
				name = b.name + "/send-back"
			}
			t.Run(name, func(t *testing.T) {
				e, f := newValidateEnv(t, b)
				e.eng.Outbox = &interruptedPRNotification{Outbox: e.eng.Outbox}
				f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: "conflicting", Behind: 1, Files: []string{"docs/contracts/cli.md"}})
				e.accepted(t)
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); err == nil || !strings.Contains(err.Error(), "interrupted before notification") {
					t.Fatalf("notification failure = %v", err)
				}
				doc := "pr-blocked-" + head + "-conflicting"
				if _, ok := e.documents(t)[doc]; !ok {
					t.Fatal("observation not recorded")
				}
				id := "app.t7." + doc
				earlier := "Pull request: \nHead: " + head + "\nBase: " + baseHead + "\nState: conflicting\nThe task waits for the Director; Guildhall does not rebase automatically."
				sendEarlier(t, e, id, store.MessagePRBlocked, earlier)

				if sendBack {
					feedback := "Resolve the PR conflict."
					e.script.Expect(buildExp(t, 2, head2, "sha256:"+sum([]byte(feedback))))
					want(t, e.advance(t, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: feedback}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
				} else {
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: "validate"})
				}

				if n := countID(t, e, id); n != 1 {
					t.Fatalf("messages under %s = %d, want 1", id, n)
				}
				if got := e.messages(t)[id]; got != earlier {
					t.Fatalf("message under %s = %q, want the earlier text", id, got)
				}
			})
		}
	}
}

// The tolerance is in notify, so a decision message sent before with other text is already reported too.
func TestDecisionMessageWithChangedTextIsAlreadyReported(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[:1]...)
			id := "app.t7.decision.accept-1"
			earlier := "Decision accept-1 is open, as an older binary put it."
			sendEarlier(t, e, id, store.MessageDecision, earlier)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			if n := countID(t, e, id); n != 1 {
				t.Fatalf("messages under %s = %d, want 1", id, n)
			}
			if got := e.messages(t)[id]; got != earlier {
				t.Fatalf("message under %s = %q, want the earlier text", id, got)
			}
		})
	}
}
