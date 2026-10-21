package launch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/store"
)

// TestRunAfterTheInterpreterTookTheResult: the interpreter's result intake (`step done`) may record the
// attempt's outcome before the launcher does. The launcher's own record of the same outcome is then a
// replay, not an error; a different outcome keeps the store's and is reported.
func TestRunAfterTheInterpreterTookTheResult(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded store.AttemptOutcome
		wantErr  bool
	}{{"same outcome", store.AttemptSucceeded, false}, {"other outcome", store.AttemptFailed, true}} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAttempt(t)
			marker := filepath.Join(t.TempDir(), "publish")
			cmd, _ := worker(t, a, artisantest.Worker{Result: a.Files().ResultFile, ResultBody: resultBody(t, a.Identity(), adapter.StatusSucceeded), ResultMarker: marker})
			done, cancel := start(a, cmd)
			defer cancel()
			running(t, a)
			withStore(t, a, func(s *store.Store) {
				if _, err := s.FinishAttempt("interp-finish", "app", "t-1", "build", 1, tc.recorded); err != nil {
					t.Fatal(err)
				}
			})
			if err := os.WriteFile(marker, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			r := wait(t, done)
			if (r.err != nil) != tc.wantErr {
				t.Errorf("Run() error = %v, want error %v", r.err, tc.wantErr)
			}
			if got := storedOutcome(t, a); got != tc.recorded {
				t.Errorf("stored outcome %q, want %q", got, tc.recorded)
			}
			if !tc.wantErr && r.r.AttemptOutcome != store.AttemptSucceeded {
				t.Errorf("report outcome %q, want succeeded", r.r.AttemptOutcome)
			}
		})
	}
}
