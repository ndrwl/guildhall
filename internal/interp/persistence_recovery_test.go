package interp_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

type interruptedDocumentRecord struct {
	interp.Records
	name string
}

func (r *interruptedDocumentRecord) PutDocument(id string, d store.DocumentRef) (store.Receipt, error) {
	if d.Name == r.name {
		return store.Receipt{}, errors.New("interrupted before document record")
	}
	return r.Records.PutDocument(id, d)
}

func TestMergeabilityRecoversFileBeforeDocumentRecord(t *testing.T) {
	for _, b := range backends {
		for _, state := range []string{"conflicting", "behind"} {
			for _, step := range []string{"validate", "deliver"} {
				t.Run(b.name+"/"+state+"/"+step, func(t *testing.T) {
					e, f := newValidateEnv(t, b)
					ev := interp.Event{Kind: interp.TaskCreated}
					if step == "deliver" {
						f.SetChecks(repoRemote, head, ciPassed...)
						want(t, e.toValidate(t), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
					} else {
						e.accepted(t)
						e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
						ev = interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1}
					}
					name := "pr-blocked-" + head + "-" + state
					e.eng.Records = &interruptedDocumentRecord{Records: e.h.Records, name: name}
					f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: state, Behind: 1, Files: []string{"first.md"}})
					if err := advanceErr(t, e, ev); err == nil || !strings.Contains(err.Error(), "interrupted before document record") {
						t.Fatalf("record failure = %v", err)
					}
					if _, ok := e.documents(t)[name]; ok {
						t.Fatal("interrupted observation was recorded")
					}
					p := filepath.Join(e.eng.DocumentDir, "tasks", "app", "t7", name+".json")
					first, err := os.ReadFile(p)
					if err != nil {
						t.Fatal(err)
					}
					e.eng.Records = e.h.Records
					f.SetMergeability(forge.Mergeability{Head: head, Base: head2, State: state, Behind: 2, Files: []string{"later.md"}})
					want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirector, Step: step})
					if d := e.documents(t)[name]; d.SHA256 != sum(first) {
						t.Fatalf("recovered observation = %+v", d)
					}
					text := e.messages(t)["app.t7."+name]
					if !strings.Contains(text, "Base: "+baseHead) || !strings.Contains(text, "Commits behind: 1") || !strings.Contains(text, "first.md") || strings.Contains(text, "later.md") {
						t.Fatalf("recovered notification = %q", text)
					}
					count := len(e.inbox(t))
					e.advance(t, interp.Event{Kind: interp.TaskCreated})
					if len(e.inbox(t)) != count {
						t.Fatal("recovery duplicated notification")
					}
				})
			}
		}
	}
}

func TestSendBackRejectsAlreadyPreparedNextAttempt(t *testing.T) {
	for _, b := range backends {
		for _, fileOnly := range []bool{false, true} {
			name := b.name + "/recorded"
			if fileOnly {
				name = b.name + "/file-only"
			}
			t.Run(name, func(t *testing.T) {
				o := bound(t, workflow.VerdictChangesRequested, head, []workflow.Finding{{ID: "f1", Rule: "S2", Summary: "Add a regression test."}})
				e := newEnvDefinition(t, b, loadWorkflow(t, "loop"),
					planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})),
					buildExp(t, 1, head, ""), reviewExp(t, "review", "reviewer", 1, head, nil, o),
					buildExp(t, 2, head2, outcomeDigest(o)))
				e.accepted(t)
				e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
				if fileOnly {
					e.eng.Records = &interruptedDocumentRecord{Records: e.h.Records, name: "build-2-inputs"}
				} else {
					e.eng.Caps = store.AttemptCaps{MaxRunning: 1}
					occupy(t, e, "other")
				}
				err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
				if fileOnly {
					if err == nil || !strings.Contains(err.Error(), "interrupted before document record") {
						t.Fatalf("prepare failure = %v", err)
					}
				} else if !errors.Is(err, store.ErrAttemptCap) {
					t.Fatalf("cap refusal = %v", err)
				}
				e.eng.Records = e.h.Records
				if err := advanceErr(t, e, interp.Event{Kind: interp.StepSentBack, Step: "build", Attempt: 1, Reason: "New Director instructions"}); !errors.Is(err, interp.ErrEvent) {
					t.Fatalf("send-back with prepared binding = %v", err)
				}
				if _, ok := e.documents(t)["send-back-build-1"]; ok {
					t.Fatal("refused feedback was recorded")
				}
				e.eng.Caps = store.AttemptCaps{}
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 2})
			})
		}
	}
}
