package artisan_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

func TestNotStartedEvidenceNamesNoProcesses(t *testing.T) {
	for name, op := range map[string]func(artisan.Containment, artisan.Identity) ([]artisan.Identity, error){
		"contained": artisan.Contained, "sweep": artisan.Sweep,
	} {
		t.Run(name, func(t *testing.T) {
			c := artisan.Containment{Kind: artisan.KindNotStarted}
			if got, err := op(c, artisan.Identity{}); err != nil || len(got) != 0 {
				t.Fatalf("explicit no-start evidence = %v, %v", got, err)
			}
			for _, bad := range []artisan.Containment{
				{Kind: artisan.KindNotStarted, Name: "unit"},
				{Kind: artisan.KindNotStarted, Reason: "uncertain"},
			} {
				if _, err := op(bad, artisan.Identity{}); err == nil {
					t.Fatal("accepted inconsistent no-start evidence")
				}
			}
			if _, err := op(c, artisan.Identity{PID: 1, Token: "worker"}); err == nil {
				t.Fatal("accepted worker with no-start evidence")
			}
		})
	}
}
