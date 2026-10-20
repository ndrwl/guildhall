package artisan_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

func platformHarness(_ *testing.T, h *artisantest.Harness) {
	h.Terminate = true
	h.SetsidEscapes = true
	h.Kind = artisan.KindGroup
}
