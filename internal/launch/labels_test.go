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

// TestSliceWorkspaceLabels: a multi-PR task's attempt is labelled with its slice, "slice k/n", and its step without
// the slice mark; a one-slice task keeps today's label; the sweep recognises both forms, and no other attempt's.
func TestSliceWorkspaceLabels(t *testing.T) {
	one := artisan.AttemptKey{Repository: "guildhall", TaskID: "d16", Step: "build", Attempt: 2}
	two := artisan.AttemptKey{Repository: "guildhall", TaskID: "d16", Step: "build--s2", Attempt: 1}
	for _, c := range []struct {
		k     artisan.AttemptKey
		total int
		want  string
	}{
		{one, 0, "└ d16 · build #2"},
		{one, 1, "└ d16 · build #2"},
		{one, 3, "└ d16 · slice 1/3 · build #2"},
		{two, 3, "└ d16 · slice 2/3 · build #1"},
	} {
		got := launch.SliceWorkspaceLabel(c.k, c.total)
		if got != c.want {
			t.Errorf("SliceWorkspaceLabel(%s, %d) = %q, want %q", c.k, c.total, got, c.want)
		}
		if !launch.IsAttemptWorkspaceLabel(c.k, got) {
			t.Errorf("IsAttemptWorkspaceLabel(%s, %q) = false", c.k, got)
		}
	}
	for _, label := range []string{"└ d16 · slice 1/3 · build #1", "└ d16 · slice 2/3 · review #1", "└ d16 · slice 2/1 · build #1", "└ d17 · slice 2/3 · build #1", "└ d16 · build--s2 #2"} {
		if launch.IsAttemptWorkspaceLabel(two, label) {
			t.Errorf("IsAttemptWorkspaceLabel(%s, %q) = true, want another attempt's", two, label)
		}
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
