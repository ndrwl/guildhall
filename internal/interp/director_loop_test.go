package interp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// stageManifest reads the manifest of attempt n of a Director stage's prompt.
func (e *env) stageManifest(t *testing.T, step string, n int) render.Manifest {
	t.Helper()
	var m render.Manifest
	data, err := os.ReadFile(filepath.Join(e.stageDir(step, n), "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLoopBackToADirectorStage runs director-loop.json: the Guildmaster's changes_requested on the research notes
// loops back to the Director's grill, which is handed to the Director as attempt 2 with the answer as feedback, its
// own notes as previous and the research notes the answer was bound to as reviewed, in its binding and its prompt.
// Research is not run again, the review is asked again on the same notes once the grill finishes, and past the
// loop's max the loop-exit decision is asked.
func TestLoopBackToADirectorStage(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			research := []byte("# Research\n\nThe list is sorted in the view.\n")
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-loop"), researchExpectation(research))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "look", Decision: "look-1"})

			words := "changes_requested\nShow the sort options as a table."
			e.answer(t, "look-1", words)
			grill2 := interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2}
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "look-1"}), grill2)

			wantInputs := []string{"feedback sha256:" + sum([]byte(words)), "previous sha256:" + sum(grillNotes), "reviewed sha256:" + sum(research)}
			var bound []string
			for _, in := range e.stageManifest(t, "grill", 2).Inputs {
				bound = append(bound, in.Port+" "+in.Digest)
			}
			if !slices.Equal(bound, wantInputs) {
				t.Errorf("grill 2 inputs = %q, want %q", bound, wantInputs)
			}
			data, err := os.ReadFile(filepath.Join(e.eng.DocumentDir, filepath.FromSlash(e.documents(t)["grill-2-inputs"].Path)))
			if err != nil {
				t.Fatal(err)
			}
			for _, port := range []string{`"feedback"`, `"previous"`, `"reviewed"`} {
				if !strings.Contains(string(data), port) {
					t.Errorf("grill 2 binding has no %s input: %s", port, data)
				}
			}
			prompt, err := os.ReadFile(filepath.Join(e.stageDir("grill", 2), "prompt", render.PromptFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []string{"- feedback: inputs/", "- previous: inputs/", "- reviewed: inputs/"} {
				if !strings.Contains(string(prompt), part) {
					t.Errorf("grill 2 prompt does not list %q", part)
				}
			}
			if _, ok := e.messages(t)["app.t7.stage.grill-2"]; !ok {
				t.Errorf("messages = %v, want the stage message for grill 2", e.messages(t))
			}

			// A hand-off that stopped before its mark replays: no second message, render or attempt.
			if err := os.Remove(filepath.Join(e.stageDir("grill", 2), "launched.json")); err != nil {
				t.Fatal(err)
			}
			msgs, renders, attempts := len(e.messages(t)), len(e.stages), len(e.attempts(t))
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), grill2)
			if len(e.messages(t)) != msgs || len(e.stages) != renders || len(e.attempts(t)) != attempts {
				t.Errorf("replay: %d messages, %d renders, %d attempts; want %d, %d, %d", len(e.messages(t)), len(e.stages), len(e.attempts(t)), msgs, renders, attempts)
			}
			if _, err := os.Stat(filepath.Join(e.stageDir("grill", 2), "launched.json")); err != nil {
				t.Errorf("replay did not mark grill 2 launched: %v", err)
			}

			notes2 := []byte("# Grill notes\n\n1. Sort by due date, shown as a table of options.\n")
			finishStage(t, e, "grill", 2, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: notes2})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), interp.Next{Wait: interp.WaitDecision, Step: "look", Decision: "look-2"})
			if d := e.decision(t, "look-2"); d.Subject == nil || d.Subject.Digest != "sha256:"+sum(research) {
				t.Errorf("look-2 bound to %+v, want the research notes", d.Subject)
			}
			if got := len(e.script.Launched()); got != 1 {
				t.Errorf("%d Artisan launches, want research once", got)
			}

			e.answer(t, "look-2", "changes_requested\nStill not a table.")
			want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "look-2"}), interp.Next{Wait: interp.WaitDecision, Step: "look", Decision: "look-x2"})
			if got := e.attempts(t); len(got) != 3 {
				t.Errorf("attempts = %+v, want no grill 3 past the loop's max", got)
			}
		})
	}
}

// TestValidateLoopToADirectorStageIsUnsupported: a validate failed loop may still go back only to an Artisan step.
func TestValidateLoopToADirectorStageIsUnsupported(t *testing.T) {
	def := loadWorkflow(t, "validate")
	def.Steps = append([]workflow.Step{loadWorkflow(t, "director-grill").Steps[0]}, def.Steps...)
	def.Steps[len(def.Steps)-2].On["failed"] = workflow.Rule{Goto: "grill", Max: 1}
	if err := workflow.Validate(def); err != nil {
		t.Fatal(err)
	}
	e, f := newValidateEnvDefinition(t, backends[0], def)
	f.SetChecks(repoRemote, head, ciFailed...)
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
	want(t, e.accepted(t), interp.Next{Wait: interp.WaitAttempt, Step: "build", Attempt: 1})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "build", Attempt: 1})
	err := advanceErr(t, e, interp.Event{Kind: interp.AttemptResult, Step: "review", Attempt: 1})
	if !errors.Is(err, interp.ErrUnsupported) || !strings.Contains(err.Error(), "director") {
		t.Errorf("validate loop to the grill = %v, want ErrUnsupported naming the director", err)
	}
}
