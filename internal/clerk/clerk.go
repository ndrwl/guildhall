// Package clerk owns the embedded Clerk Claude plugin: the Guildmaster's read-only slash commands, each of which
// runs one `guildhall clerk` subcommand and echoes its output (docs/clerk.md). The plugin holds no logic; its
// answers come from the binary. It never changes workflow state.
package clerk

import (
	"embed"
	"io/fs"
)

//go:embed all:plugin
var plugin embed.FS

// Commands are the plugin's slash commands, each running the `guildhall clerk` subcommand of the same name.
var Commands = []string{"guild", "decisions", "task", "inspect", "queue"}

// Files returns independent copies of the shipped plugin assets, relative to its root. Tests are not installed.
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
