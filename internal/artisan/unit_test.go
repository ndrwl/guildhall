package artisan_test

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

func TestMain(m *testing.M) {
	artisantest.MainIfWorker()
	os.Exit(m.Run())
}

// TestUnitContract runs the containment contract against this platform's
// backend; platformHarness sets what the backend's platform can and cannot do.
func TestUnitContract(t *testing.T) {
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, err := artisan.NewUnit("contract-" + rand.Text()[:8])
		if err != nil {
			t.Fatal(err)
		}
		return u
	})
	platformHarness(&h)
	artisantest.RunUnitContract(t, h)
}

// TestFakeUnitContract keeps the double the launcher's tests use honest.
func TestFakeUnitContract(t *testing.T) {
	artisantest.RunUnitContract(t, newFakeHarness())
}
