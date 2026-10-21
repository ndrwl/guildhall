//go:build unix

package artisan_test

import (
	"runtime"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// A process-group record whose group id now names another process holds
// nothing: Sweep signals no one.
func TestSweepRefusesAReusedGroupLeader(t *testing.T) {
	stranger, _ := startedProcess(t)
	kind := artisan.KindGroup
	if runtime.GOOS == "linux" {
		kind = artisan.KindSubreaper
	}
	swept, err := artisan.Sweep(artisan.Containment{Kind: kind}, artisan.Identity{PID: stranger.PID, Token: stranger.Token + "x"})
	if err != nil || len(swept) != 0 {
		t.Errorf("Sweep of a reused group id = %v, %v; want nothing", swept, err)
	}
	if lv, err := artisan.Probe(stranger); lv != artisan.Alive {
		t.Errorf("the process at the reused group id is %v (%v)", lv, err)
	}
}
