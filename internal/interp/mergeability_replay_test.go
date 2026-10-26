package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/messaging"
	"github.com/ndrwl/guildhall/internal/store"
)

type interruptedPRNotification struct {
	interp.Outbox
	failed bool
}

func (o *interruptedPRNotification) Send(to store.Recipient, e messaging.Envelope) (store.Receipt, error) {
	if e.Type == store.MessagePRBlocked && !o.failed {
		o.failed = true
		return store.Receipt{}, errors.New("interrupted before notification")
	}
	return o.Outbox.Send(to, e)
}

func TestMergeabilityReplaysRecordedObservationAfterNotificationFailure(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.eng.Outbox = &interruptedPRNotification{Outbox: e.eng.Outbox}
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: "conflicting", Behind: 1, Files: []string{"docs/contracts/cli.md"}})
			e.accepted(t)
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			if err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}); err == nil || !strings.Contains(err.Error(), "interrupted before notification") {
				t.Fatalf("notification failure = %v", err)
			}
			if _, ok := e.documents(t)["pr-blocked-"+head+"-conflicting"]; !ok {
				t.Fatal("observation not durable before notification")
			}
			// The external condition clears between the record and the recovered send.
			f.SetMergeability(forge.Mergeability{})
			f.SetChecks(repoRemote, head, ciPassed...)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
			id := "app.t7.pr-blocked-" + head + "-conflicting"
			if !strings.Contains(e.messages(t)[id], baseHead) {
				t.Fatalf("recorded observation was not replayed: %v", e.messages(t))
			}
			count := len(e.inbox(t))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if len(e.inbox(t)) != count {
				t.Fatal("recovery duplicated notification")
			}
		})
	}
}
