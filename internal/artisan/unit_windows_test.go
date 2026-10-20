package artisan_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
)

func platformHarness(_ *testing.T, h *artisantest.Harness) {
	h.BreakawayRefused = true
	h.Kind = artisan.KindJob
}
