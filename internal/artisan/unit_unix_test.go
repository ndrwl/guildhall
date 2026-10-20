//go:build unix

package artisan_test

import "github.com/ndrwl/guildhall/internal/artisan/artisantest"

func platformHarness(h *artisantest.Harness) {
	h.Terminate = true
	h.SetsidEscapes = true
}
