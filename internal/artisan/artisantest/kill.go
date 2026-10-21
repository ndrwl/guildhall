package artisantest

import "github.com/ndrwl/guildhall/internal/artisan"

// KillVerified ends id only while a live process still has its PID and
// token; it is artisan.Kill.
func KillVerified(id artisan.Identity) error { return artisan.Kill(id) }
