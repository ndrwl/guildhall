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

func TestMergeabilityReplayIgnoresWorkflowDocuments(t *testing.T) {
	for _, b := range backends {
		for _, step := range []string{"pr-blocked-build", "pr-blocked-abcd-conflicting", "pr-blocked-build-behind"} {
			t.Run(b.name+"/"+step, func(t *testing.T) {
				def := straight(t)
				for i := range def.Steps {
					s := &def.Steps[i]
					if s.ID == "build" {
						s.ID = step
					}
					for port, refs := range s.Inputs {
						for j, ref := range refs {
							if suffix, ok := strings.CutPrefix(ref, "build."); ok {
								s.Inputs[port][j] = step + "." + suffix
							}
						}
					}
				}
				exps := straightScript(t)
				exps[1].Step = step
				e := newEnvDefinition(t, b, def, exps...)
				want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: step, Attempt: 1})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: step, Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "review", Attempt: 1})
				want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"})
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "sign-off", Decision: "sign-off-1"})
				for _, name := range []string{step + "-1-inputs", step + "-1-head"} {
					if _, ok := e.documents(t)[name]; !ok {
						t.Fatalf("workflow document %s was not recorded", name)
					}
				}
				page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, message := range page.Messages {
					if message.Type == store.MessagePRBlocked {
						t.Fatalf("workflow document emitted a PR-blocked notification: %+v", message)
					}
				}
			})
		}
	}
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
