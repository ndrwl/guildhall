package interp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestRefuteBeforeAcceptance(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := straight(t)
			attack := workflow.Step{ID: "refute", Type: "refute", Version: 1, Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"reviewer"`)}, Inputs: map[string]workflow.Refs{"spec": {"plan.spec"}, "context": {"research.notes"}}}
			research := workflow.Step{ID: "research", Type: "research", Version: 1, Settings: map[string]json.RawMessage{"profile": json.RawMessage(`"researcher"`)}}
			def.Steps = append([]workflow.Step{research, def.Steps[0], attack}, def.Steps[1:]...)
			def.Steps[3].Inputs["findings"] = workflow.Refs{"refute.findings"}
			if err := workflow.Validate(def); err != nil {
				t.Fatal(err)
			}
			notes := []byte("# Research\nUse a bounded queue.\n")
			findings := []byte("# Refutation\n\n- F1 | high | Unbounded queue growth exhausts memory.\n  Suggested spec change: bound the queue and define overflow handling.\n")
			e := newEnvDefinition(t, b, def, researchExpectation(notes), planExpectation(t, interptest.Succeed("plan", interptest.File{Path: "spec.md", Data: specV1(t)})), interptest.Expectation{Step: "refute", Attempt: 1, Profile: "reviewer", Inputs: map[string]string{"spec": "sha256:" + sum(specV1(t)), "context": "sha256:" + sum(notes)}, Respond: interptest.Succeed("attacked plan", interptest.File{Path: "findings.md", Data: findings})})
			root := filepath.Join(t.TempDir(), "baseline")
			if err := baseline.Install(root); err != nil {
				t.Fatal(err)
			}
			e.eng.Prompter = interp.PromptFunc(func(stage render.Stage, inputs []render.Input) (render.Rendered, error) {
				if stage.Step.Type != "refute" {
					return e.prompt(stage, inputs)
				}
				source := func(name string) render.Source {
					return render.Source{Name: name, Path: filepath.Join(root, filepath.FromSlash(name))}
				}
				return render.Prompt(stage, inputs, render.Baseline{Role: source("roles/artisan.md"), Guide: source("stages/refute.md")}, nil)
			})
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "refute", Attempt: 1})
			// Recovery while refuting must not create another launch or an early approval.
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "refute", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			if len(e.attempts(t)) != 3 {
				t.Fatal("replay duplicated an attempt")
			}
			docs := e.documents(t)
			f := docs["refute-1-findings"]
			data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(f.Path)))
			if err != nil || string(data) != string(findings) {
				t.Fatalf("findings not persisted: %q %v", data, err)
			}
			decision := e.decision(t, "accept-1")
			if decision.Subject == nil || *decision.Subject != (store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + sum(specV1(t))}) {
				t.Fatalf("approval not bound to spec: %+v", decision)
			}
			for _, text := range []string{"spec:", "findings:", sum(specV1(t)), sum(findings)} {
				if !strings.Contains(decision.Question, text) {
					t.Errorf("approval does not show %q: %s", text, decision.Question)
				}
			}
		})
	}
}
