package interp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// grillNotes is what the Director writes as the grill's notes in these tests.
var grillNotes = []byte("# Grill notes\n\n1. Sort by due date? Yes, the Guildmaster agreed.\n")

// finishStage plays the Director finishing attempt n of a Director stage: each file is written into the task's
// working directory and the result is published at the attempt's result file, as `step finish` does.
func finishStage(t *testing.T, e *env, step string, n int, status adapter.Status, files ...interptest.File) {
	t.Helper()
	work := filepath.Join(e.eng.StateDir, "app", "t7", "work")
	var outputs []adapter.Output
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(f.Path)), f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, adapter.Output{Path: f.Path})
	}
	id := adapter.Identity{TaskID: "t7", AttemptID: step + "-" + strconv.Itoa(n)}
	if err := adapter.WriteResult(filepath.Join(e.eng.StateDir, "app", "t7", step+"-"+strconv.Itoa(n), "result.json"),
		adapter.Result{Identity: id, Status: status, Summary: "the Director finished " + step, Outputs: outputs}); err != nil {
		t.Fatal(err)
	}
}

// stageDir is attempt n of step's attempt directory.
func (e *env) stageDir(step string, n int) string {
	return filepath.Join(e.eng.StateDir, "app", "t7", step+"-"+strconv.Itoa(n))
}

// researchExpectation is the research Artisan, answering with its notes.
func researchExpectation(notes []byte) interptest.Expectation {
	return interptest.Expectation{Step: "research", Attempt: 1, Profile: "researcher", Inputs: map[string]string{}, Standards: []string{"standards/go.md"},
		Respond: interptest.Succeed("researched", interptest.File{Path: "notes.md", Data: notes})}
}

// TestDirectorStageWaitsWithoutLaunching: a director step records attempt 1 with its binding, prepares its prompt
// with the Director's role, the stage guide and the step's skills, marks it launched by the Director with no
// process, sends the Director one director-stage message and waits for it, launching nothing; repeating the
// advance renders, records and sends nothing new.
func TestDirectorStageWaitsWithoutLaunching(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"))
			stage := interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), stage)

			if got := e.attempts(t); len(got) != 1 || got[0].Step != "grill" || got[0].Number != 1 || got[0].Outcome != "" {
				t.Fatalf("attempts = %+v, want grill 1 running", got)
			}
			if got := e.script.Launched(); len(got) != 0 {
				t.Fatalf("launched %d attempts, want none", len(got))
			}
			if e.documents(t)["grill-1-inputs"].Path == "" {
				t.Error("grill 1 has no recorded binding")
			}
			dir := e.stageDir("grill", 1)
			var m render.Manifest
			data, err := os.ReadFile(filepath.Join(dir, "prompt", "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if m.Role.Path != "roles/director.md" || m.Guide.Path != "stages/grill.md" || len(m.Skills) != 1 || m.Skills[0].Name != "go-tdd" {
				t.Errorf("manifest role %s, guide %s, skills %+v; want the Director's role, the grill guide and go-tdd", m.Role.Path, m.Guide.Path, m.Skills)
			}
			prompt := filepath.Join(dir, "prompt", render.PromptFile)
			if _, err := os.Stat(prompt); err != nil {
				t.Errorf("prompt: %v", err)
			}
			var mark map[string]any
			data, err = os.ReadFile(filepath.Join(dir, "launched.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &mark); err != nil {
				t.Fatal(err)
			}
			if mark["launcher"] != "director" || mark["Ref"] != nil || mark["ref"] != nil {
				t.Errorf("launched.json = %s, want launcher director and no process", data)
			}

			msgs := e.messages(t)
			text, ok := msgs["app.t7.stage.grill-1"]
			if !ok || len(msgs) != 1 {
				t.Fatalf("Director messages = %v, want only app.t7.stage.grill-1", msgs)
			}
			for _, part := range []string{"grill", prompt, "notes", "guildhall step finish"} {
				if !strings.Contains(text, part) {
					t.Errorf("stage message does not name %q:\n%s", part, text)
				}
			}
			m2, err := e.mailbox.Read("app.t7.stage.grill-1")
			if err != nil || m2.Type != store.MessageDirectorStage {
				t.Errorf("stage message type %q, %v; want %q", m2.Type, err, store.MessageDirectorStage)
			}

			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), stage)
			if len(e.stages) != 1 || len(e.attempts(t)) != 1 || len(e.messages(t)) != 1 || len(e.script.Launched()) != 0 {
				t.Errorf("replay: %d renders, %d attempts, messages %v, %d launches; want one render, one attempt, one message, no launch",
					len(e.stages), len(e.attempts(t)), e.messages(t), len(e.script.Launched()))
			}
		})
	}
}

// TestDirectorStageReplaysAfterACrashBeforeTheMark: an advance that stopped after publishing the preparation, before
// launched.json, hands the same preparation to the Director again without rendering it again, and the message is
// a replay of the one sent.
func TestDirectorStageReplaysAfterACrashBeforeTheMark(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			mark := filepath.Join(e.stageDir("grill", 1), "launched.json")
			prompt := filepath.Join(e.stageDir("grill", 1), "prompt", render.PromptFile)
			before, err := os.ReadFile(prompt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(mark); err != nil {
				t.Fatal(err)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			if _, err := os.Stat(mark); err != nil {
				t.Errorf("replay did not mark the stage launched: %v", err)
			}
			after, err := os.ReadFile(prompt)
			if err != nil || string(after) != string(before) || len(e.stages) != 1 {
				t.Errorf("replay rendered again: %d renders, prompt changed %v, %v", len(e.stages), string(after) != string(before), err)
			}
			if got := e.messages(t); len(got) != 1 || len(e.attempts(t)) != 1 {
				t.Errorf("replay: messages %v, attempts %+v", got, e.attempts(t))
			}
		})
	}
}

// TestDirectorStageResultMovesTheTaskOn is the live grill's transcript with the director actor: the Director's
// succeeded result records grill.notes and the task moves to research, whose notes and the grill's are the plan's
// context; replaying the result writes nothing new.
func TestDirectorStageResultMovesTheTaskOn(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			research := []byte("# Research\n\nThe list is sorted in the view.\n")
			plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			plan.Inputs = map[string]string{"context": "sha256:" + sum(research)}
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation(research), plan)
			e.advance(t, interp.Event{Kind: interp.TaskCreated})

			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			docs := e.documents(t)
			if d := docs["grill-1-notes"]; d.SHA256 != sum(grillNotes) {
				t.Fatalf("grill-1-notes = %+v, want the Director's notes", d)
			}
			if got := e.attempts(t); got[0].Outcome != store.AttemptSucceeded {
				t.Errorf("grill 1 outcome = %q", got[0].Outcome)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if got := len(e.documents(t)); got != len(docs) {
				t.Errorf("replayed result recorded %d documents, want %d", got, len(docs))
			}

			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
			launched := e.script.Launched()
			var context []string
			for _, in := range launched[len(launched)-1].Manifest.Inputs {
				if in.Port == "context" {
					context = append(context, in.Digest)
				}
			}
			if exp := []string{"sha256:" + sum(grillNotes), "sha256:" + sum(research)}; !slices.Equal(context, exp) {
				t.Errorf("plan context = %q, want the grill's and research's notes %q", context, exp)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1}), interp.Next{Wait: interp.WaitDecision, Step: "accept", Decision: "accept-1"})
		})
	}
}

// TestFailedDirectorStageRetries: a Director stage finished failed waits for the Director like a failed Artisan
// attempt, and step retry hands attempt 2 to the Director as the first was handed.
func TestFailedDirectorStageRetries(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation([]byte("notes\n")))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusFailed)
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitDirector, Step: "grill", Attempt: 1})
			if _, ok := e.messages(t)["app.t7.attempt-failed.grill-1"]; !ok {
				t.Errorf("messages = %v, want attempt-failed for grill 1", e.messages(t))
			}

			want(t, e.advance(t, retried("grill", 1, "the Guildmaster is back")), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2})
			msgs := e.messages(t)
			if _, ok := msgs["app.t7.stage.grill-2"]; !ok || msgs["app.t7.attempt-retried.grill-1"] != "the Guildmaster is back" {
				t.Errorf("messages = %v, want the stage message for grill 2 and the retry's reason", msgs)
			}
			if _, err := os.Stat(filepath.Join(e.stageDir("grill", 2), "launched.json")); err != nil {
				t.Errorf("grill 2 not handed to the Director: %v", err)
			}
			if len(e.script.Launched()) != 0 {
				t.Errorf("a retried Director stage launched an Artisan")
			}
			finishStage(t, e, "grill", 2, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			if d := e.documents(t)["grill-2-notes"]; d.SHA256 != sum(grillNotes) {
				t.Errorf("grill-2-notes = %+v", d)
			}
		})
	}
}

// TestDirectorStageAfterAnArtisanStage: a Director stage reached after an Artisan stage's result is handed to the
// Director in the same advance, and its result moves the task on to the plan.
func TestDirectorStageAfterAnArtisanStage(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			def := loadWorkflow(t, "director-grill")
			def.Name = "research-then-grill"
			def.Steps[0], def.Steps[1] = def.Steps[1], def.Steps[0]
			if err := workflow.Validate(def); err != nil {
				t.Fatal(err)
			}
			research := []byte("research notes\n")
			plan := planExpectation(t, interptest.Succeed("planned", interptest.File{Path: "spec.md", Data: specV1(t)}))
			plan.Inputs = map[string]string{"context": "sha256:" + sum(research)}
			e := newEnvDefinition(t, b, def, researchExpectation(research), plan)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "research", Attempt: 1}), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 1})
			if _, ok := e.messages(t)["app.t7.stage.grill-1"]; !ok {
				t.Errorf("messages = %v", e.messages(t))
			}
			finishStage(t, e, "grill", 1, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 1}), interp.Next{Wait: interp.WaitAttempt, Step: "plan", Attempt: 1})
		})
	}
}

// TestAnyAdvanceTakesAPublishedDirectorStageResult: a Director stage has no launcher to announce its result, so an
// advance that stopped after `step finish` published the result is recovered by any later advance, as by task
// start: a succeeded result moves the task on and a failed one waits for the Director, each recorded once.
func TestAnyAdvanceTakesAPublishedDirectorStageResult(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnvDefinition(t, b, loadWorkflow(t, "director-grill"), researchExpectation([]byte("notes\n")))
			e.advance(t, interp.Event{Kind: interp.TaskCreated})
			finishStage(t, e, "grill", 1, adapter.StatusFailed)
			failed := interp.Next{Wait: interp.WaitDirector, Step: "grill", Attempt: 1}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), failed)
			if got := e.attempts(t); got[0].Outcome != store.AttemptFailed {
				t.Errorf("grill 1 outcome = %q, want failed", got[0].Outcome)
			}
			if _, ok := e.messages(t)["app.t7.attempt-failed.grill-1"]; !ok {
				t.Errorf("messages = %v, want attempt-failed for grill 1", e.messages(t))
			}

			want(t, e.advance(t, retried("grill", 1, "again")), interp.Next{Wait: interp.WaitDirectorStage, Step: "grill", Attempt: 2})
			finishStage(t, e, "grill", 2, adapter.StatusSucceeded, interptest.File{Path: "notes.md", Data: grillNotes})
			research := interp.Next{Wait: interp.WaitAttempt, Step: "research", Attempt: 1}
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), research)
			docs := e.documents(t)
			if d := docs["grill-2-notes"]; d.SHA256 != sum(grillNotes) {
				t.Errorf("grill-2-notes = %+v, want the Director's notes", d)
			}
			want(t, e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "grill", Attempt: 2}), research)
			if got := len(e.documents(t)); got != len(docs) || len(e.script.Launched()) != 1 {
				t.Errorf("replay: %d documents, want %d; %d launches, want 1", got, len(docs), len(e.script.Launched()))
			}
		})
	}
}
