package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/steward"
)

// TestSelfBinary: a bootstrap places the Steward's own binary only when it is a release, with the home's launcher.
func TestSelfBinary(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	exe := filepath.Join(dir, "guildhall")
	writeFile(t, exe, "binary")
	h := release.Home{Dir: home, GOOS: runtime.GOOS}
	if err := os.MkdirAll(filepath.Dir(h.Launcher()), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, h.Launcher(), "launcher")
	report := ""
	app := &App{Executable: func() (string, error) { return exe, nil },
		Getenv: func(k string) string {
			if k == release.HomeEnv {
				return home
			}
			return ""
		},
		Output: func(_ context.Context, path string, args ...string) ([]byte, error) { return []byte(report), nil }}

	report = `{"version":"dev","modified":false,"release_contract":1}`
	if _, err := app.selfBinary(); err == nil || !strings.Contains(err.Error(), "not a release") {
		t.Errorf("dev build: %v, want refused", err)
	}
	report = `{"version":"v0.9.0","modified":true,"release_contract":1}`
	if _, err := app.selfBinary(); err == nil || !strings.Contains(err.Error(), "not a release") {
		t.Errorf("modified build: %v, want refused", err)
	}
	report = `{"version":"v0.9.0","modified":false,"release_contract":1}`
	self, err := app.selfBinary()
	if err != nil {
		t.Fatalf("selfBinary: %v", err)
	}
	if want, _ := filepath.EvalSymlinks(exe); self.Path != want || self.Launcher != h.Launcher() || self.Version != "v0.9.0" || self.GOOS != runtime.GOOS || self.GOARCH != runtime.GOARCH {
		t.Errorf("selfBinary = %+v", self)
	}
}

// TestRenderBootstrapPlan: a bootstrap plan says it only places Guildhall, names the binary and launcher targets and
// the next dry run, and shows no roster change.
func TestRenderBootstrapPlan(t *testing.T) {
	p := &steward.SetupPlan{Kind: steward.SetupKindProvision, Digest: "sha256:00", Machine: "vds", Director: "ops", CopyDir: "/home/g/ops",
		SSHAlias: "guild-vds", PlanDir: "/p", Files: []steward.PlannedFile{{Name: "files/bootstrap/guildhall", Target: "/home/g/.local/bin/guildhall", SHA256: "ab"}},
		Bootstrap: &steward.Bootstrap{Version: "v0.9.0", Platform: "linux/amd64", Guildhall: "/home/g/.local/bin/guildhall", Launcher: "/home/g/.local/bin/guildhall-launcher"},
		Steps:     []steward.SetupStep{{ID: steward.StepInstall, Kind: steward.StepKindRemote, Argv: steward.InstallFromCommand("/home/g/.local/bin/guildhall", "v0.9.0", "/home/g/.local/bin/guildhall", "ab"), Expect: "installed"}}}
	out := renderSetupPlan(p, "/s/config.toml")
	for _, want := range []string{
		"vds (guild-vds): a bare machine, with nothing at guildhall_path /home/g/.local/bin/guildhall\n",
		"this plan only bootstraps Guildhall: it copies this Steward's own guildhall v0.9.0 (linux/amd64)",
		"make the dry run again with the same request to plan Director ops at /home/g/ops",
		"ssh -o BatchMode=yes -- guild-vds /home/g/.local/bin/guildhall release install v0.9.0 --from /home/g/.local/bin/guildhall --sha256 ab",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered plan lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "roster.toml:") {
		t.Errorf("a bootstrap plan shows a roster change:\n%s", out)
	}
}
