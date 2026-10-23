package claude_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter/claude"
)

func TestDirectorCannotPassGrantRules(t *testing.T) {
	claudetest.Install(t)
	spec := testSpec(t)
	if code := claude.Main([]string{"--task", spec.TaskID, "--attempt", spec.AttemptID, "--prompt", spec.PromptFile, "--result", spec.ResultFile, "--allow-tool", "Bash"}, io.Discard, io.Discard); code != 2 {
		t.Fatalf("raw grant exit = %d, want usage error 2", code)
	}
}

func grantProfile(t *testing.T, rules []string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if rules == nil {
		rules = []string{}
	}
	data, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	text := "schema_version = 1\nmode = \"director\"\nname = \"test\"\n[profile.builder]\nadapter = \"claude\"\nallowed_tools = " + string(data) + "\n[[repository]]\nid = \"app\"\npath = \"/src/app\"\nremote = \"git@example.com:app.git\"\nowner_machine = \"test\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, "builder"
}

func TestWrapperProfileAuthority(t *testing.T) {
	claudetest.Install(t)
	for _, name := range []string{"empty", "missing", "no-copy", "no-profile"} {
		t.Run(name, func(t *testing.T) {
			spec := testSpec(t)
			spec.Copy, spec.Profile = grantProfile(t, nil)
			switch name {
			case "missing":
				spec.Profile = "undeclared"
			case "no-copy":
				spec.Copy = ""
			case "no-profile":
				spec.Profile = ""
			}
			err := claude.Run(context.Background(), args(spec), io.Discard, io.Discard)
			if name == "missing" {
				if err == nil || !strings.Contains(err.Error(), "undeclared") {
					t.Fatalf("unknown profile: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := claudetest.Granted(spec.ResultFile)
			if err != nil || len(got) != 0 {
				t.Fatalf("granted %q: %v", got, err)
			}
			if _, err := os.Stat(claude.ArtisanSettingsPath(spec.ResultFile)); !os.IsNotExist(err) {
				t.Fatalf("settings: %v", err)
			}
		})
	}
}
