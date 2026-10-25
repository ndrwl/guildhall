package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
)

// Explicit heads still use the existing content validation, not Git fallback;
// an omitted document output is still an error. These are green-first controls.
func TestHeadFallbackScope(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name+"/document still required", func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Succeed("no document")))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			_, err := e.eng.Advance(context.Background(), interp.Event{Repository: "app", Task: "t7", Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if err == nil || !strings.Contains(err.Error(), "no output for port spec") {
				t.Fatalf("document intake: %v", err)
			}
		})
		t.Run(b.name+"/explicit head still validated", func(t *testing.T) {
			exps := straightScript(t)[:2]
			exps[1].Respond = interptest.Succeed("bad head", interptest.File{Path: "head", Data: []byte("not-a-commit\n")})
			e := newEnv(t, b, exps...)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			e.answer(t, next.Decision, "approved")
			e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: next.Decision})
			_, err := e.eng.Advance(context.Background(), interp.Event{Repository: "app", Task: "t7", Kind: interp.AttemptResult, Step: "build", Attempt: 1})
			if err == nil || !strings.Contains(err.Error(), "code head output is not") {
				t.Fatalf("explicit intake: %v", err)
			}
			if _, ok := e.documents(t)["build-1-head"]; ok {
				t.Fatal("invalid head recorded")
			}
		})
	}
}
