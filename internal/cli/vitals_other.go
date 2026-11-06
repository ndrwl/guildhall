//go:build !(linux || darwin || windows)

package cli

import "github.com/ndrwl/guildhall/internal/web"

// osVitals gives no vitals on an OS Guildhall does not read them on: each shows as unavailable.
func osVitals(string) web.Vitals { return web.Vitals{} }
