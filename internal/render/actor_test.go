package render

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestDirectorPromptHasNoArtisanCompletion(t *testing.T) {
	for _, kind := range workflow.Kinds() {
		if !slices.Contains(kind.Actors, workflow.DirectorActor) {
			continue
		}
		t.Run(kind.Type, func(t *testing.T) {
			stage := attempt(t, step(t, "step", kind.Type, `{"actor":"director"}`))
			result, err := Prompt(stage, nil, baseline("grill"), nil)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(result.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, instruction := range []string{"## Required outputs", "## Result template", "result file your runtime names"} {
				if strings.Contains(string(data), instruction) {
					t.Errorf("Director prompt contains Artisan instruction %q", instruction)
				}
			}
		})
	}
}

// Both review subjects use the same actor boundary and verdict contract.
func TestReviewActors(t *testing.T) {
	for _, kind := range []string{"review", "document-review"} {
		for _, actor := range []string{"artisan", "guildmaster"} {
			t.Run(kind+"/"+actor, func(t *testing.T) {
				stage := attempt(t, step(t, "review", kind, `{"actor":"`+actor+`"}`))
				ask, questionErr := Question(stage, nil)
				result, promptErr := Prompt(stage, nil, baseline("build"), nil)
				if actor == "guildmaster" {
					if questionErr != nil || !slices.Equal(ask.Options, []string{"approved", "changes_requested", "rejected"}) || !strings.Contains(ask.Question, "("+kind+" version 1)") {
						t.Fatalf("question = %+v, error = %v", ask, questionErr)
					}
					if !errors.Is(promptErr, ErrWrongActor) {
						t.Fatalf("prompt error = %v", promptErr)
					}
					if _, err := os.Stat(stage.Dir); !os.IsNotExist(err) {
						t.Fatalf("refused prompt created attempt directory: %v", err)
					}
				} else {
					if !errors.Is(questionErr, ErrWrongActor) {
						t.Fatalf("question error = %v", questionErr)
					}
					if promptErr != nil || result.Manifest.Identity.Kind != kind {
						t.Fatalf("prompt = %+v, error = %v", result, promptErr)
					}
				}
			})
		}
	}
}
