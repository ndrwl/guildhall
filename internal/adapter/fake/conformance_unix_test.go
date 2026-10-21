//go:build unix

package fake

import (
	"os"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
)

// TestFakeRuntimeConformanceInAPane runs the suite with the fake typed into a
// tab by adapter.TabLauncher, quoted for the pane's POSIX shell and stopped by
// Ctrl+C, as `artisan start --herdr` runs it. CI has no Herdr server, so the
// panes are adaptertest.ShellPanes, which keeps the Herdr tab contract.
func TestFakeRuntimeConformanceInAPane(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunConformance(t, func(t *testing.T) adaptertest.Subject {
		panes := adaptertest.NewShellPanes(t, "conformance", "w1")
		return adaptertest.Subject{
			Runtime:      Adapter{Executable: exe},
			Capabilities: adapter.Capabilities{},
			Prompt:       func(b adaptertest.Behaviour) string { return prompts[b] },
			Start:        adaptertest.StartInPane(adapter.TabLauncher{Host: panes, WorkspaceID: "w1", Poll: 10 * time.Millisecond}),
		}
	})
}
