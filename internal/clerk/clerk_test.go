package clerk

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFiles: the plugin is its manifest and one command per Clerk subcommand, with no tests installed.
func TestFiles(t *testing.T) {
	files := Files()
	var manifest map[string]any
	if err := json.Unmarshal(files[".claude-plugin/plugin.json"], &manifest); err != nil || manifest["name"] != "clerk" || manifest["version"] == nil || manifest["description"] == nil {
		t.Fatalf("manifest: %v %v", manifest, err)
	}
	want := map[string]bool{".claude-plugin/plugin.json": true}
	for _, c := range Commands {
		want["commands/"+c+".md"] = true
	}
	for name := range files {
		if strings.HasPrefix(name, "tests/") || !want[name] {
			t.Errorf("unexpected plugin file %s", name)
		}
	}
	for name := range want {
		if len(files[name]) == 0 {
			t.Errorf("missing plugin file %s", name)
		}
	}
	// Each call returns its own copies.
	files[".claude-plugin/plugin.json"][0] = 'x'
	if Files()[".claude-plugin/plugin.json"][0] != '{' {
		t.Error("Files shares its bytes between calls")
	}
}

// TestCommandsAllowOnlyTheirSubcommand: each command may run exactly its own read-only subcommand, never
// guildhall as a whole, runs it with the command's arguments and echoes its output verbatim.
func TestCommandsAllowOnlyTheirSubcommand(t *testing.T) {
	files := Files()
	for _, c := range Commands {
		body := string(files["commands/"+c+".md"])
		front, rest, ok := strings.Cut(strings.TrimPrefix(body, "---\n"), "\n---\n")
		if !strings.HasPrefix(body, "---\n") || !ok {
			t.Fatalf("%s: no frontmatter:\n%s", c, body)
		}
		var allowed []string
		for line := range strings.Lines(front) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "allowed-tools:"); ok {
				allowed = append(allowed, strings.TrimSpace(v))
			}
		}
		if want := "Bash(guildhall clerk " + c + ":*)"; len(allowed) != 1 || allowed[0] != want {
			t.Errorf("%s: allowed-tools %q, want exactly %q", c, allowed, want)
		}
		if !strings.Contains(rest, "`guildhall clerk "+c+" $ARGUMENTS`") || !strings.Contains(rest, "verbatim, and no commentary") ||
			!strings.Contains(rest, "reply with only its error output") {
			t.Errorf("%s: body does not run its one command and echo it:\n%s", c, rest)
		}
	}
}
