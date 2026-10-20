package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

func TestRenderRejectsInvalidSettingsBeforeInstallation(t *testing.T) {
	for _, tc := range []struct{ name, kind, settings, diagnostic string }{
		{"skills-string", "review", `{"actor":"artisan","skills":"guildhall-grill-notes"}`, "skills"},
		{"skills-null", "review", `{"actor":"artisan","skills":null}`, "skills"},
		{"skills-name", "review", `{"actor":"artisan","skills":["../skill"]}`, "skills"},
		{"skills-duplicate", "review", `{"actor":"artisan","skills":["skill","skill"]}`, "skills"},
		{"profile-type", "review", `{"actor":"artisan","profile":42}`, "profile"},
		{"profile-name", "review", `{"actor":"artisan","profile":"../profile"}`, "profile"},
		{"instructions-type", "review", `{"actor":"artisan","instructions":[]}`, "instructions"},
		{"actor-type", "review", `{"actor":42}`, "actor"},
		{"actor-disallowed", "review", `{"actor":"director"}`, "actor"},
		{"actor-missing", "review", `{}`, "actor"},
		{"actor-single-kind", "plan", `{"actor":"artisan"}`, "actor"},
		{"unknown-setting", "review", `{"actor":"artisan","typo":true}`, "typo"},
		{"skills-disallowed-kind", "grill", `{"actor":"artisan","skills":["guildhall-grill-notes"]}`, "skills"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			var settings map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.settings), &settings); err != nil {
				t.Fatal(err)
			}
			stage := render.Stage{Repository: "repo", Task: "task", Attempt: 1, Dir: filepath.Join(tmp, "attempt"), Step: workflow.Step{ID: "step", Type: tc.kind, Version: 1, Settings: settings}}
			request, err := json.Marshal(map[string]any{"Stage": stage})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(tmp, "request.json")
			if err := os.WriteFile(path, request, 0600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			called := false
			app := New()
			app.Stdout, app.Stderr = &stdout, &stderr
			app.InstallSkills = func(string) error { called = true; return errors.New("unexpected install") }
			if code := app.Run([]string{"render", path}); code != 2 {
				t.Fatalf("exit %d: %s", code, stderr.String())
			}
			if called {
				t.Error("installed before validation")
			}
			if stdout.Len() != 0 {
				t.Errorf("emitted output: %s", stdout.String())
			}
			if !strings.Contains(stderr.String(), tc.diagnostic) {
				t.Errorf("missing setting diagnostic: %s", stderr.String())
			}
			if _, err := os.Stat(stage.Dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("attempt exists or stat failed: %v", err)
			}
		})
	}
}
