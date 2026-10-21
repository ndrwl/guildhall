package artisan_test

import (
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// TestAttemptKeyLayout pins the attempt layout the interpreter prepares and
// the launcher runs in, with literal paths, so neither can drift alone.
func TestAttemptKeyLayout(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	for _, tc := range []struct {
		key     artisan.AttemptKey
		id, dir string
	}{
		{artisan.AttemptKey{Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}, "build-1", filepath.Join(state, "app", "t-1", "build-1")},
		// A hyphenated step id: the attempt id is the step, a hyphen and the number.
		{artisan.AttemptKey{Repository: "docs-site", TaskID: "t7", Step: "sign-off", Attempt: 12}, "sign-off-12", filepath.Join(state, "docs-site", "t7", "sign-off-12")},
	} {
		if got := tc.key.AttemptID(); got != tc.id {
			t.Errorf("%v.AttemptID() = %q, want %q", tc.key, got, tc.id)
		}
		if got := tc.key.Dir(state); got != tc.dir {
			t.Errorf("%v.Dir() = %q, want %q", tc.key, got, tc.dir)
		}
	}
	if artisan.ResultFile != "result.json" || artisan.ModelDir != "model" {
		t.Errorf("ResultFile = %q, ModelDir = %q; want result.json and model", artisan.ResultFile, artisan.ModelDir)
	}
}
