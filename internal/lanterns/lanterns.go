// Package lanterns owns the embedded Guildhall Claude plugin, its status band and
// advisory vitals identities. It never changes workflow state.
package lanterns

import (
	"embed"
	"fmt"
	"io/fs"
)

//go:embed all:plugin
var plugin embed.FS

// Files returns independent copies of the shipped plugin assets, relative to its
// root. Tests are not installed. No external checkout or symlink is needed.
func Files() map[string][]byte {
	out := map[string][]byte{}
	_ = fs.WalkDir(plugin, "plugin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "plugin/tests" {
				return fs.SkipDir
			}
			return nil
		}
		b, err := plugin.ReadFile(path)
		if err != nil {
			return err
		}
		out[path[len("plugin/"):]] = b
		return nil
	})
	return out
}

// Counts is one read-only Guildhall status snapshot. Known=false is explicitly
// neutral, never interpreted as a healthy empty store.
type Counts struct {
	Known                                bool
	Tasks, Running, Decisions, Attention int
}

// Band formats the first segment as the overall light: attention takes priority
// over decisions, then a known healthy snapshot. Counts never come from Firstmate.
func Band(c Counts) string {
	if !c.Known {
		return "⚪ status unavailable"
	}
	light := "🟢"
	if c.Decisions > 0 {
		light = "🟡"
	}
	if c.Attention > 0 {
		light = "🔴"
	}
	return fmt.Sprintf("%s tasks %d · running %d · decisions %d · attention %d", light, c.Tasks, c.Running, c.Decisions, c.Attention)
}
