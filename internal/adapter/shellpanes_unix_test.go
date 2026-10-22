//go:build unix

package adapter_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
)

// TestShellPanesContract runs the terminal boundary's contract test against
// adaptertest.ShellPanes, so a runtime that passes the conformance suite in a
// shell pane has passed it over a host that keeps the Herdr tab contract.
func TestShellPanesContract(t *testing.T) {
	adapter.RunTabHostContract(t, func(t *testing.T) adapter.TabHostHarness {
		p := adaptertest.NewShellPanes(t, "lab", "w1")
		return adapter.TabHostHarness{Host: p, Foreign: adaptertest.NewShellPanes(t, "other-lab", "w1"), Session: "lab", Sent: p.Sent,
			Reopen: func() adapter.TabHost { return p.Reopen() }}
	})
}
