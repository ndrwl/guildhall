package interp_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ndrwl/guildhall/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestHumanDocumentReviewOutput(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := straight(t)
			review := workflow.Step{ID: "doc-review", Type: "document-review", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"guildmaster"`)}, Inputs: map[string][]string{"subject": {"plan.spec"}}}
			def.Steps = append(def.Steps[:1], append([]workflow.Step{review}, def.Steps[1:]...)...)
			def.Steps[2].Inputs["reviews"] = []string{"doc-review.outcome"}
			e := newEnvDefinition(t, b, def, planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)})))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			next := e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
			if next.Decision != "doc-review-1" {
				t.Fatalf("next = %+v", next)
			}
			e.answer(t, next.Decision, "approved")
			e.eng.StandardsRevision = "standards-2"
			e.eng.Records = &failOutcomeRecord{Records: e.h.Records}
			if _, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.DecisionAnswered, Repository: "app", Task: "t7", Decision: "doc-review-1"}); err == nil {
				t.Fatal("expected injected failure")
			}
			for range 2 {
				next = e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "doc-review-1"})
				if next.Decision != "accept-1" {
					t.Fatalf("next = %+v", next)
				}
			}
			doc := e.documents(t)["doc-review-1-outcome"]
			data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, doc.Path))
			if err != nil {
				t.Fatal(err)
			}
			outcome, err := workflow.ParseReviewOutcome(data)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Reviewer != workflow.HumanReviewer || outcome.Revisions.Subject.DocumentDigest != "sha256:"+sum(specV1(t)) || outcome.Revisions.StandardsRevision != "standards-1" {
				t.Fatalf("outcome = %+v", outcome)
			}
		})
	}
}

type failOutcomeRecord struct {
	interp.Records
	failed bool
}

func (r *failOutcomeRecord) PutDocument(id string, doc store.DocumentRef) (store.Receipt, error) {
	if strings.HasSuffix(doc.Name, "-outcome") && !r.failed {
		r.failed = true
		return store.Receipt{}, errors.New("crash before outcome record")
	}
	return r.Records.PutDocument(id, doc)
}

func TestOutputContainment(t *testing.T) {
	for _, mode := range []string{"file", "parent", "supplemental"} {
		t.Run(mode, func(t *testing.T) {
			files := []interptest.File{{Path: "spec.md", Data: specV1(t)}}
			if mode == "parent" {
				files[0].Path = "nested/spec.md"
			}
			if mode == "supplemental" {
				files = append(files, interptest.File{Path: "extra.md", Data: []byte("extra")})
			}
			e := newEnv(t, backends[0], planExpectation(t, interptest.Succeed("planned", files...)))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			outside := t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "spec.md"), []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			work := filepath.Join(e.eng.StateDir, "app", "t7", "work")
			target, link := filepath.Join(outside, "spec.md"), filepath.Join(work, files[len(files)-1].Path)
			if mode == "parent" {
				target, link = outside, filepath.Join(work, "nested")
			}
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			_, err := e.eng.Advance(context.Background(), interp.Event{Kind: interp.AttemptResult, Repository: "app", Task: "t7", Step: "plan", Attempt: 1})
			if err == nil {
				t.Fatal("accepted escaping output")
			}
			if len(e.documents(t)) != 0 {
				t.Fatal("recorded outputs before validating containment")
			}
		})
	}
}
