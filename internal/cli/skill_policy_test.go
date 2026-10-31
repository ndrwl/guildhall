package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/skills"
)

func TestPreflightClaudePolicy(t *testing.T) {
	for _, version := range []string{"2.1.310 (Claude Code)", "2.1.309 (Claude Code)", "garbage"} {
		t.Run(version, func(t *testing.T) {
			dir := directorCopy(t, "[director]\nadapter='claude'\n[profile.worker]\nadapter='claude'\n", "/exit")
			a, out, stderr := newTestApp(map[string]string{"claude": "/stub/claude", "guildhall": "/stub/guildhall"}, nil)
			a.Executable = os.Executable
			a.InstallSkills = skills.Install
			a.InstallBaseline = baseline.Install
			probes := 0
			a.Output = func(_ context.Context, _ string, args ...string) ([]byte, error) {
				probes++
				if len(args) != 1 || args[0] != "--version" {
					t.Fatalf("unexpected model call %v", args)
				}
				return []byte(version), nil
			}
			code := a.Run([]string{"director", "start", "--check", "--copy", dir})
			if version != "2.1.310 (Claude Code)" {
				if code != 1 || !strings.Contains(out.String()+stderr.String(), "2.1.310") {
					t.Fatalf("must refuse unsupported version: %d %s %s", code, out, stderr)
				}
				return
			}
			if code != 0 || probes != 1 {
				t.Fatalf("check %d probes=%d %s %s", code, probes, out, stderr)
			}
			for _, want := range []string{"Director director-v1", "profile worker artisan-v1", "17 release skills", "account sync disabled", "not live discovery", "not isolated", "cc-plugin-plugin-authoring@builtin", "source=default", "overridable=true", "rendered JSON validated"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q: %s", want, out)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, "director")); !os.IsNotExist(err) {
				t.Fatalf("check created session: %v", err)
			}
		})
	}
}
