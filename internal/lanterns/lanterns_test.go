package lanterns

import (
	"encoding/json"
	"testing"
)

func TestFilesAreSelfContained(t *testing.T) {
	files := Files()
	for _, name := range []string{".claude-plugin/plugin.json", "hooks/hooks.json", "hooks/register.js", "hooks/context.cjs", "lib/presentation.mjs"} {
		if len(files[name]) == 0 {
			t.Errorf("missing embedded plugin file %s", name)
		}
	}
	var manifest map[string]any
	if err := json.Unmarshal(files[".claude-plugin/plugin.json"], &manifest); err != nil || manifest["name"] != "lanterns" {
		t.Fatalf("manifest: %v %v", manifest, err)
	}
}

func TestBandPriorityAndUnknown(t *testing.T) {
	for _, tt := range []struct {
		in   Counts
		want string
	}{
		{Counts{}, "⚪ status unavailable"},
		{Counts{Known: true}, "🟢 tasks 0 · running 0 · decisions 0 · attention 0"},
		{Counts{Known: true, Tasks: 3, Running: 2, Decisions: 1}, "🟡 tasks 3 · running 2 · decisions 1 · attention 0"},
		{Counts{Known: true, Decisions: 1, Attention: 2}, "🔴 tasks 0 · running 0 · decisions 1 · attention 2"},
	} {
		if got := Band(tt.in); got != tt.want {
			t.Errorf("Band(%+v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
