package artisan_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

// TestStopContract runs the stop ladder and precedence contract with this
// platform's backend.
func TestStopContract(t *testing.T) {
	artisantest.RunStopContract(t, nativeHarness(t))
}

// TestFakeStopContract runs it with fakeUnit, the launcher's double.
func TestFakeStopContract(t *testing.T) {
	artisantest.RunStopContract(t, newFakeHarness())
}
