package steward_test

// Takeover, provisioning and bootstrap of a native Windows machine, on the stand-in fleet: the roster's laptop, whose
// guildhall_path is C:/Users/guild/bin/guildhall.exe and whose paths are Windows paths with forward slashes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/setup"
	"github.com/ndrwl/guildhall/internal/steward"
)

const (
	winAlias     = "guild-laptop"
	winGuildhall = "C:/Users/guild/bin/guildhall.exe"
	winLauncher  = "C:/Users/guild/bin/guildhall-launcher.exe"
	winCopy      = "C:/Users/guild/directors/ops"
	winSetup     = "C:/Users/guild/AppData/Local/Guildhall/setup"
	winInfra     = "C:/Users/guild/src/infra"
	winConfig    = `schema_version = 1
mode = "director"
name = "ops"

[[repository]]
id = "infra"
path = "C:/Users/guild/src/infra"
remote = "https://example.com/infra.git"
owner_machine = "laptop"
`
)

// TestTakeoverWindows: a running Director copy on the Windows laptop is taken over with the roster's guildhall.exe
// and its Windows copy directory, and its entry is committed under the laptop; a Linux copy path there is refused.
func TestTakeoverWindows(t *testing.T) {
	fx := newTakeoverFixture(t, "running")
	win := newTakeoverCopy(fx.f, winAlias, winCopy, winConfig, "running")
	fx.f.Handle(winAlias, win.handle)
	s := fx.setup()
	planDir := filepath.Join(t.TempDir(), "takeover-laptop-ops")
	p, err := s.PlanTakeover(context.Background(), steward.TakeoverInput{Machine: "laptop", Director: "ops", CopyDir: winCopy, PlanDir: planDir})
	if err != nil {
		t.Fatalf("PlanTakeover: %v", err)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatal(err)
	}
	if p.Guildhall != winGuildhall || p.Roster.OS != steward.OSWindows {
		t.Errorf("plan program %s, os %s", p.Guildhall, p.Roster.OS)
	}
	for _, st := range p.Steps {
		if st.Kind == steward.StepKindRemote && (st.Argv[0] != winGuildhall || steward.CheckCommand(st.Argv) != nil) {
			t.Errorf("step %s runs %q", st.ID, st.Argv)
		}
	}
	if want := steward.ExportCommand(winGuildhall, winCopy+"/state/steward/takeover/takeover-laptop-ops", winCopy); !slices.Equal(p.Steps[2].Argv, want) {
		t.Errorf("backup step %q, want %q", p.Steps[2].Argv, want)
	}
	if out, err := s.ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	if win.state != "running" || win.steward != "steward" || win.authorized == "" {
		t.Errorf("copy after takeover: %+v", win)
	}
	roster, err := steward.ParseRoster([]byte(fx.git(fx.checkout, "show", "HEAD:roster.toml") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if laptop, _ := roster.Machine("laptop"); len(laptop.Directors) != 2 || laptop.Directors[1].CopyDir != winCopy {
		t.Errorf("laptop in the committed roster: %+v", laptop)
	}

	for name, in := range map[string]steward.TakeoverInput{
		"a Linux path on Windows":           {Machine: "laptop", Director: "ops2", CopyDir: "/home/guild/directors/ops"},
		"a Windows path on Linux":           {Machine: "workstation", Director: "ops2", CopyDir: winCopy},
		"a roster machine of another OS":    {Machine: "laptop", Director: "ops2", CopyDir: "C:/x/ops", OS: steward.OSLinux},
		"a new macOS machine":               {Machine: "mac", Director: "ops2", CopyDir: "/Users/guild/ops", SSHAlias: "guild-mac", OS: steward.OSDarwin},
		"a backslash path on a new machine": {Machine: "desk", Director: "ops2", CopyDir: `C:\Users\guild\ops`, SSHAlias: "guild-desk", OS: steward.OSWindows},
	} {
		in.PlanDir = filepath.Join(t.TempDir(), "plan")
		if _, err := s.PlanTakeover(context.Background(), in); err == nil {
			t.Errorf("%s: planned", name)
		}
	}
}

// TestTakeoverNewWindowsMachine: a Windows machine not yet in the roster is taken over with --os windows, and its new
// [[machine]] table says so; applying the plan read back from disk plans it the same.
func TestTakeoverNewWindowsMachine(t *testing.T) {
	fx := newTakeoverFixture(t, "no session")
	win := newTakeoverCopy(fx.f, "guild-desk", winCopy, strings.Replace(winConfig, `"laptop"`, `"desk"`, 1), "no session")
	fx.f.Handle("guild-desk", win.handle)
	s := fx.setup()
	planDir := filepath.Join(t.TempDir(), "takeover-desk")
	p, err := s.PlanTakeover(context.Background(), steward.TakeoverInput{Machine: "desk", Director: "ops", CopyDir: winCopy, SSHAlias: "guild-desk", OS: steward.OSWindows, PlanDir: planDir})
	if err != nil {
		t.Fatalf("PlanTakeover: %v", err)
	}
	if !p.Roster.NewMachine || p.Roster.OS != steward.OSWindows || !strings.Contains(p.RosterDiff, `+os = "windows"`) {
		t.Errorf("roster entry %+v, diff:\n%s", p.Roster, p.RosterDiff)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatal(err)
	}
	if out, err := s.ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	roster, err := steward.ParseRoster([]byte(fx.git(fx.checkout, "show", "HEAD:roster.toml") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if desk, ok := roster.Machine("desk"); !ok || desk.OS != steward.OSWindows {
		t.Errorf("desk in the committed roster: %+v", desk)
	}
}

// newWindowsProvisionFixture is the provisioning fixture aimed at the Windows laptop, with Go and Herdr pinned to
// their Windows builds.
func newWindowsProvisionFixture(t *testing.T, open ...string) *provisionFixture {
	t.Helper()
	fx := newProvisionFixture(t)
	fx.m = newProvisionMachine(fx.f, winAlias, open...)
	fx.m.goos, fx.m.copyDir = "windows", winCopy
	fx.f.Handle(winAlias, fx.m.handle)
	fx.request = fmt.Sprintf(`schema_version = 1
machine = "laptop"
director = "ops"
copy_dir = %q
setup_dir = %q
herdr_session = "guild"
step_files = ["herdr", "go", "sign-in"]

[pins]
herdr_version = "0.5.0"
herdr_url = "https://example.com/herdr-0.5.0-windows-amd64.exe"
herdr_sha256 = "%s"
go_version = "1.27.2"
go_url = "https://go.dev/dl/go1.27.2.windows-amd64.zip"
go_sha256 = "%s"

[[repository]]
id = "infra"
remote = "git@example.com:guild/infra.git"
path = %q
`, winCopy, winSetup, strings.Repeat("ab", 32), strings.Repeat("cd", 32), winInfra)
	return fx
}

// TestProvisionWindows: on the Windows laptop the doctor checks the built-ins' Windows forms, given Windows paths,
// every remote command runs guildhall.exe with them, and the apply ends with a managed copy whose config.toml names
// the Windows clone path.
func TestProvisionWindows(t *testing.T) {
	fx := newWindowsProvisionFixture(t, "go", "gh-sign-in")
	planDir := filepath.Join(t.TempDir(), "provision-laptop-ops")
	p := fx.dryRun(t, planDir)
	if want := []string{"copy-dir", "go", "gh-sign-in", "config", "managed", "store", "clone-infra", "adopt", "enrol", "reload", "start", "roster", "summary"}; !slices.Equal(stepIDs(p), want) {
		t.Errorf("steps %v, want %v", stepIDs(p), want)
	}
	for _, r := range p.Doctor {
		if r.OS != "windows" {
			t.Errorf("doctor report %s is for %s", r.Steps, r.OS)
		}
	}
	if d := p.Doctor[slices.IndexFunc(p.Doctor, func(r setup.Report) bool { return r.Steps == "director" })]; !slices.Equal(d.Skipped, []string{"tailscale-ssh"}) {
		t.Errorf("director report skipped %v, want tailscale-ssh only", d.Skipped)
	}
	if pre := p.Preflight; pre == nil || !slices.Equal(pre.Sets, []string{"copy_dir=" + winCopy, "clone_paths=" + winInfra}) {
		t.Errorf("preflight %+v", p.Preflight)
	}
	for _, st := range p.Steps {
		switch st.Kind {
		case steward.StepKindRemote, steward.StepKindFix:
			if err := steward.CheckCommand(st.Argv); err != nil {
				t.Errorf("step %s: %v", st.ID, err)
			}
			if st.Argv[0] != winGuildhall && st.Argv[0] != "git" {
				t.Errorf("step %s runs %s", st.ID, st.Argv[0])
			}
		case steward.StepKindWizard:
			if st.Argv[0] != winGuildhall || !slices.Contains(st.Argv, winSetup) {
				t.Errorf("wizard %s: %q", st.ID, st.Argv)
			}
		}
	}
	cfg, err := os.ReadFile(filepath.Join(planDir, "files", "config.toml"))
	if err != nil || !strings.Contains(string(cfg), `path = "`+winInfra+`"`) {
		t.Errorf("config.toml %q, %v", cfg, err)
	}
	s := fx.setup()
	ctx := context.Background()
	if out, err := s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeWaiting || out.Step != "gh-sign-in" {
		t.Fatalf("first apply = %v, %v; want waiting gh-sign-in", out, err)
	}
	delete(fx.m.open, "gh-sign-in")
	if out, err := s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply after the wizard = %v, %v", out, err)
	}
	if fx.m.steward != "steward" || fx.m.tab != "t-1" || fx.m.acted["go"] != 1 || !fx.m.has(winInfra+"/.git/HEAD") {
		t.Errorf("machine after provisioning: %+v", fx.m)
	}
}

// TestProvisionWindowsRefusesLinuxPaths: a Linux path for a Windows machine, and clone paths equal but for letter
// case, which Windows ignores, are refused before the machine is reached.
func TestProvisionWindowsRefusesLinuxPaths(t *testing.T) {
	for name, edit := range map[string]func(r *steward.ProvisionRequest){
		"a Linux copy_dir":    func(r *steward.ProvisionRequest) { r.CopyDir = newCopy },
		"a Linux setup_dir":   func(r *steward.ProvisionRequest) { r.SetupDir = newSetup },
		"a Linux clone path":  func(r *steward.ProvisionRequest) { r.Repositories[0].Path = infraPath },
		"a clone path's case": func(r *steward.ProvisionRequest) { r.Repositories[0].Path = strings.ToUpper(winCopy) },
	} {
		fx := newWindowsProvisionFixture(t)
		r := fx.req(t)
		edit(r)
		if _, err := fx.setup().PlanProvision(context.Background(), r, filepath.Join(t.TempDir(), "plan")); err == nil {
			t.Errorf("%s: planned", name)
		}
		if calls := fx.f.Calls(); len(calls) != 0 {
			t.Errorf("%s: reached the machine: %q", name, calls)
		}
	}
}

// TestBootstrapWindows: a bare Windows machine is bootstrapped from the cross-built release in bootstrap_from, read
// without running it: guildhall.exe and guildhall-launcher.exe are copied beside each other, checked with certutil
// -hashfile before the binary first runs, and installed.
func TestBootstrapWindows(t *testing.T) {
	fx := newWindowsProvisionFixture(t)
	dist := t.TempDir()
	for name, data := range map[string]string{"guildhall.exe": "guildhall v0.9.0 windows binary\n", "guildhall-launcher.exe": "launcher windows binary\n"} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fx.request = strings.Replace(fx.request, "herdr_session = \"guild\"\n", "herdr_session = \"guild\"\nbootstrap = true\nbootstrap_from = "+fmt.Sprintf("%q", dist)+"\n", 1)
	var hashed []string
	fx.f.Handle(winAlias, func(argv []string) ([]byte, bool, error) {
		if argv[0] == "certutil" {
			hashed = append(hashed, argv[2])
			data, ok := fx.f.Read(steward.Remote{Alias: winAlias, Path: argv[2]})
			if !ok {
				return nil, true, fmt.Errorf("CertUtil: -hashfile command FAILED: 0x80070002 (WIN32: 2 ERROR_FILE_NOT_FOUND)\nCertUtil: The system cannot find the file specified.")
			}
			return []byte("SHA256 hash of " + argv[2] + ":\r\n" + digest.Of(data).Hex() + "\r\nCertUtil: -hashfile command completed successfully.\r\n"), true, nil
		}
		if argv[1] == "release" && argv[2] == "install" {
			return []byte("installed\n"), true, nil
		}
		return fx.m.handle(argv)
	})
	builds := map[string]steward.BuildInfo{
		filepath.Join(dist, "guildhall.exe"):          {Package: "github.com/ndrwl/guildhall/cmd/guildhall", Version: "v0.9.0", GOOS: "windows", GOARCH: "amd64"},
		filepath.Join(dist, "guildhall-launcher.exe"): {Package: "github.com/ndrwl/guildhall/cmd/guildhall-launcher", Version: "v0.9.0", GOOS: "windows", GOARCH: "amd64"},
	}
	s := fx.setup()
	s.Self = func() (steward.SelfBinary, error) {
		return steward.SelfBinary{}, errors.New("the Steward's own binary is not used")
	}
	s.ReadBuild = func(file string) (steward.BuildInfo, error) { return builds[file], nil }
	planDir := filepath.Join(t.TempDir(), "bootstrap-laptop")
	// The laptop's guildhall.exe is missing, so the dry run plans the bootstrap.
	p, err := s.PlanProvision(context.Background(), fx.req(t), planDir)
	if err != nil {
		t.Fatalf("PlanProvision: %v", err)
	}
	b := p.Bootstrap
	if b == nil || b.Platform != "windows/amd64" || b.Guildhall != winGuildhall || b.Launcher != winLauncher || !strings.Contains(p.Steps[0].Expect, "certutil") {
		t.Fatalf("bootstrap %+v, steps %+v", b, p.Steps)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatal(err)
	}
	if out, err := s.ApplySetup(context.Background(), planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	if data, _ := fx.f.Read(steward.Remote{Alias: winAlias, Path: winLauncher}); string(data) != "launcher windows binary\n" {
		t.Errorf("launcher on the machine %q", data)
	}
	if !slices.Contains(hashed, winGuildhall) {
		t.Errorf("certutil hashed %q", hashed)
	}

	for name, c := range map[string]struct {
		file string
		info steward.BuildInfo
		want string
	}{
		"a Linux build":      {"guildhall.exe", steward.BuildInfo{Package: "github.com/ndrwl/guildhall/cmd/guildhall", Version: "v0.9.0", GOOS: "linux", GOARCH: "amd64"}, "built for linux"},
		"another program":    {"guildhall.exe", steward.BuildInfo{Package: "example.com/evil", Version: "v0.9.0", GOOS: "windows", GOARCH: "amd64"}, "not github.com/ndrwl/guildhall/cmd/guildhall"},
		"modified source":    {"guildhall-launcher.exe", steward.BuildInfo{Package: "github.com/ndrwl/guildhall/cmd/guildhall-launcher", GOOS: "windows", GOARCH: "amd64", Modified: true}, "modified"},
		"no release version": {"guildhall.exe", steward.BuildInfo{Package: "github.com/ndrwl/guildhall/cmd/guildhall", GOOS: "windows", GOARCH: "amd64"}, "not a release version"},
		"another arch":       {"guildhall-launcher.exe", steward.BuildInfo{Package: "github.com/ndrwl/guildhall/cmd/guildhall-launcher", GOOS: "windows", GOARCH: "arm64"}, "launcher for windows/arm64"},
	} {
		bad := map[string]steward.BuildInfo{}
		for k, v := range builds {
			bad[k] = v
		}
		bad[filepath.Join(dist, c.file)] = c.info
		s.ReadBuild = func(file string) (steward.BuildInfo, error) { return bad[file], nil }
		fresh := newWindowsProvisionFixture(t)
		fresh.f.Handle(winAlias, func(argv []string) ([]byte, bool, error) {
			return nil, true, errors.New("CertUtil: -hashfile command FAILED: 0x80070002 (WIN32: 2 ERROR_FILE_NOT_FOUND)")
		})
		s.Runner, s.Transport = fresh.f, fresh.f
		if _, err := s.PlanProvision(context.Background(), fx.req(t), filepath.Join(t.TempDir(), "plan")); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want one naming %q", name, err, c.want)
		}
	}
}

// TestParseHash: sha256sum's line, and certutil's output on current and older Windows, which spaces the bytes.
func TestParseHash(t *testing.T) {
	sum := strings.Repeat("0a", 32)
	spaced := strings.TrimSpace(strings.Repeat("0A ", 32))
	for _, c := range []struct {
		goos steward.OS
		out  string
		ok   bool
	}{
		{steward.OSLinux, sum + "  /home/guild/.local/bin/guildhall\n", true},
		{steward.OSWindows, "SHA256 hash of C:/x/guildhall.exe:\r\n" + sum + "\r\nCertUtil: -hashfile command completed successfully.\r\n", true},
		{steward.OSWindows, "SHA256 hash of file C:/x/guildhall.exe:\r\n" + spaced + "\r\nCertUtil: -hashfile command completed successfully.\r\n", true},
		{steward.OSWindows, "CertUtil: -hashfile command completed successfully.\r\n", false},
		{steward.OSLinux, "sha256sum: oops\n", false},
	} {
		got, ok := steward.ParseHash(c.goos, []byte(c.out))
		if ok != c.ok || (ok && got != sum) {
			t.Errorf("ParseHash(%s, %q) = %q, %v", c.goos, c.out, got, ok)
		}
	}
}

// TestReadBuild reads this test binary's own build information, of a package that is not Guildhall's, so a
// bootstrap from it is refused.
func TestReadBuild(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := steward.ReadBuild(exe)
	if err != nil {
		t.Fatalf("ReadBuild: %v", err)
	}
	if b.GOOS == "" || b.GOARCH == "" || b.Package == "github.com/ndrwl/guildhall/cmd/guildhall" {
		t.Errorf("ReadBuild = %+v", b)
	}
	if _, err := steward.ReadBuild(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("ReadBuild of a missing file succeeded")
	}
}
