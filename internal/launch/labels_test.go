package launch_test

import (
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/launch"
)

func TestPresentationLabels(t *testing.T) {
	k := artisan.AttemptKey{Repository: "guildhall", TaskID: "d16", Step: "build", Attempt: 2}
	if got, want := launch.WorkspaceLabel(k), "└ d16 · build #2"; got != want {
		t.Errorf("WorkspaceLabel = %q, want %q", got, want)
	}
	if got, want := launch.TabLabel(k), "artisan guildhall/d16/build/2"; got != want {
		t.Errorf("TabLabel = %q, want %q", got, want)
	}
	if got, want := launch.AttemptWorkspaceLabels(k), []string{"└ d16 · build #2", "artisan guildhall/d16/build/2"}; !slices.Equal(got, want) {
		t.Errorf("AttemptWorkspaceLabels = %q, want %q", got, want)
	}
}

// TestFinishLineStatesTheConfiguredDelay: the bridge's finish line names the effective
// artisan_workspace_close_after, and says the Herdr workspace stays open when it is 0.
func TestFinishLineStatesTheConfiguredDelay(t *testing.T) {
	const done = "guildhall: Artisan launcher finished; result and work remain in Guildhall. "
	for _, tc := range []struct {
		after time.Duration
		want  string
	}{
		{5 * time.Minute, done + "This Herdr workspace closes automatically in about 5m0s (artisan_workspace_close_after); set it to 0 to keep Herdr workspaces open."},
		{90 * time.Second, done + "This Herdr workspace closes automatically in about 1m30s (artisan_workspace_close_after); set it to 0 to keep Herdr workspaces open."},
		{0, done + "This Herdr workspace stays open (artisan_workspace_close_after is 0); close it yourself when done."},
		{-1, done + "This Herdr workspace closes automatically after the configured delay (artisan_workspace_close_after)."},
	} {
		if got := launch.FinishLine(tc.after); got != tc.want {
			t.Errorf("FinishLine(%v) = %q, want %q", tc.after, got, tc.want)
		}
	}
}
