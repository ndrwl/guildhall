package launch_test

import (
	"slices"
	"testing"

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
