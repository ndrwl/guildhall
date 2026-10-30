package claude_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// profileRules are the builder's own allowed_tools in the task grant tests' copy.
var profileRules = []string{"Bash(go test:*)", "Bash(git commit:*)"}

// effectiveRules are the builder's effective grant without a task grant: profileRules and the guildhall CILogRule.
var effectiveRules = append(slices.Clone(profileRules), claude.CILogRule)

// grantCopy is a copy whose builder grants profileRules, with a store holding task app/task-1 and, when rules is not
// nil, its approved one-attempt grant g1 of rules for build, bound to attempt build-1 by its binding document.
// It returns the copy, the spec of build-1 and the binding's path.
func grantCopy(t *testing.T, rules []string) (string, adapter.Spec, string) {
	t.Helper()
	spec := testSpec(t)
	spec.Copy, spec.Profile = grantProfile(t, profileRules)
	spec.Repository, spec.TaskID, spec.AttemptID = "app", "task-1", "build-1"
	withStore(t, spec.Copy, func(s *store.Store) error {
		_, err := s.CreateTask("task", store.Task{Repository: "app", ID: "task-1", Title: "Probe",
			Workflow:     workflow.Revision{Name: "default", Digest: "sha256:" + strings.Repeat("a", 64)},
			ConfigDigest: "sha256:" + strings.Repeat("c", 64)})
		return err
	})
	binding := filepath.Join(spec.Copy, "state", "documents", "tasks", "app", "task-1", "build-1.inputs.json")
	if rules == nil {
		return spec.Copy, spec, binding
	}
	b := map[string]any{"inputs": []any{}}
	withStore(t, spec.Copy, func(s *store.Store) error {
		g := store.TaskGrant{Repository: "app", TaskID: "task-1", ID: "g1", Step: "build", Rules: rules, Reason: "The probe needs them.", Until: store.GrantUntilAttempt}
		if _, err := s.CreateTaskGrant("grant", g, []string{"build"}); err != nil {
			return err
		}
		if _, err := s.AnswerDecision("answer", "app", "task-1", "grant-g1", "approve"); err != nil {
			return err
		}
		if _, err := s.BindTaskGrant("bind", "app", "task-1", "g1", "build", 1); err != nil {
			return err
		}
		b["grant"] = store.BoundGrant{ID: "g1", Rules: rules}
		data, err := json.Marshal(b)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(binding), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(binding, data, 0o600); err != nil {
			return err
		}
		_, err = s.PutDocument("binding", store.DocumentRef{Repository: "app", TaskID: "task-1", Name: "build-1-inputs",
			Path: "tasks/app/task-1/build-1.inputs.json", SHA256: digest.Of(data).Hex()})
		return err
	})
	return spec.Copy, spec, binding
}

func withStore(t *testing.T, copyDir string, fn func(*store.Store) error) {
	t.Helper()
	s, err := store.Open(filepath.Join(copyDir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(s); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

// runGranted runs the wrapper for spec, afresh as a resume would, and returns the rules Claude was granted.
func runGranted(t *testing.T, spec adapter.Spec) ([]string, string) {
	t.Helper()
	for _, f := range []string{spec.ResultFile, claude.ArtisanSettingsPath(spec.ResultFile)} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	var stderr bytes.Buffer
	if err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run = %v; stderr %s", err, stderr.String())
	}
	got, err := claudetest.Granted(spec.ResultFile)
	if err != nil {
		t.Fatal(err)
	}
	return got, stderr.String()
}

// TestRunAddsTheBoundTaskGrant: the bound grant's rules come last, a rule already granted keeping its place; a resume
// of the attempt after it finished keeps them, and a revoke drops them at the next run.
func TestRunAddsTheBoundTaskGrant(t *testing.T) {
	claudetest.Install(t)
	copyDir, spec, _ := grantCopy(t, []string{"Bash(go test:*)", "Bash(go:*)", "Bash(claude:*)"})
	want := append(slices.Clone(effectiveRules), "Bash(go:*)", "Bash(claude:*)")
	got, stderr := runGranted(t, spec)
	if !slices.Equal(got, want) {
		t.Errorf("granted %q; want %q", got, want)
	}
	if !strings.Contains(stderr, "task grant g1 adds") {
		t.Errorf("stderr = %q", stderr)
	}
	settings, err := os.ReadFile(claude.ArtisanSettingsPath(spec.ResultFile))
	if err != nil || !strings.Contains(string(settings), `"Bash(claude:*)"`) {
		t.Errorf("settings = %s, %v", settings, err)
	}

	withStore(t, copyDir, func(s *store.Store) error {
		if _, err := s.StartAttempt("start", "app", "task-1", "build"); err != nil {
			return err
		}
		_, err := s.FinishAttempt("finish", "app", "task-1", "build", 1, store.AttemptStopped)
		return err
	})
	if got, _ := runGranted(t, spec); !slices.Equal(got, want) {
		t.Errorf("resumed after the attempt finished: granted %q; want %q", got, want)
	}

	withStore(t, copyDir, func(s *store.Store) error {
		_, err := s.RevokeTaskGrant("revoke", "app", "task-1", "g1", "Done with it.")
		return err
	})
	got, stderr = runGranted(t, spec)
	if !slices.Equal(got, effectiveRules) {
		t.Errorf("after the revoke: granted %q; want %q", got, effectiveRules)
	}
	if !strings.Contains(stderr, "task grant g1 is revoked") {
		t.Errorf("stderr = %q", stderr)
	}
}

// TestRunWithoutATaskGrant: no store, no binding, a binding without a grant, an attempt id of another form and an
// attempt with no profile all run as before; a copy with no store says so on stderr.
func TestRunWithoutATaskGrant(t *testing.T) {
	claudetest.Install(t)
	for name, edit := range map[string]func(*adapter.Spec){
		"no binding": func(*adapter.Spec) {},
		"no store": func(s *adapter.Spec) {
			if err := os.Remove(filepath.Join(s.Copy, "director.store")); err != nil {
				t.Fatal(err)
			}
		},
		"other attempt id": func(s *adapter.Spec) { s.AttemptID = "attempt" },
	} {
		t.Run(name, func(t *testing.T) {
			_, spec, _ := grantCopy(t, nil)
			edit(&spec)
			got, stderr := runGranted(t, spec)
			if !slices.Equal(got, effectiveRules) {
				t.Errorf("granted %q; want %q", got, effectiveRules)
			}
			if said := strings.Contains(stderr, "no director.store"); said != (name == "no store") {
				t.Errorf("stderr = %q", stderr)
			}
		})
	}
	t.Run("no profile", func(t *testing.T) {
		_, spec, binding := grantCopy(t, []string{"Bash(go:*)"})
		if err := os.WriteFile(binding, []byte("tampered"), 0o600); err != nil {
			t.Fatal(err)
		}
		spec.Profile = ""
		if got, _ := runGranted(t, spec); len(got) != 0 {
			t.Errorf("granted %q; want none", got)
		}
	})
}

// TestRunRefusesAnUnverifiedTaskGrant: a binding that does not match its digest, is missing, or names a grant the
// store does not hold, and a store that cannot be read, fail the attempt before Claude starts.
func TestRunRefusesAnUnverifiedTaskGrant(t *testing.T) {
	claudetest.Install(t)
	for name, edit := range map[string]func(t *testing.T, copyDir, binding string){
		"tampered binding": func(t *testing.T, _, binding string) {
			data, err := os.ReadFile(binding)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(binding, bytes.Replace(data, []byte("Bash(go:*)"), []byte("Bash(sh:*)"), 1), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"missing binding": func(t *testing.T, _, binding string) {
			if err := os.Remove(binding); err != nil {
				t.Fatal(err)
			}
		},
		"unreadable store": func(t *testing.T, copyDir, _ string) {
			if err := os.WriteFile(filepath.Join(copyDir, "director.store"), []byte("not a store"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyDir, spec, binding := grantCopy(t, []string{"Bash(go:*)"})
			edit(t, copyDir, binding)
			err := claude.Run(context.Background(), args(spec), &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "task grant") {
				t.Fatalf("Run = %v; want a task grant error", err)
			}
			if _, err := os.Stat(claude.ArtisanSettingsPath(spec.ResultFile)); !os.IsNotExist(err) {
				t.Errorf("settings written before the refusal: %v", err)
			}
			if _, err := os.Stat(claude.ModelDir(spec.ResultFile)); !os.IsNotExist(err) {
				t.Errorf("model directory prepared before the refusal: %v", err)
			}
		})
	}
}

func TestWithTaskGrant(t *testing.T) {
	base := grants(claude.SourceDefault, "Bash(git status:*)", "Bash(go:*)")
	got := claude.WithTaskGrant(base, []string{"Bash(go:*)", "Bash(claude:*)"})
	want := append(grants(claude.SourceDefault, "Bash(git status:*)", "Bash(go:*)"), grants(claude.SourceTask, "Bash(claude:*)")...)
	if !slices.Equal(got, want) {
		t.Errorf("WithTaskGrant = %+v; want %+v", got, want)
	}
	if len(base) != 2 {
		t.Errorf("WithTaskGrant changed its input: %+v", base)
	}
}
