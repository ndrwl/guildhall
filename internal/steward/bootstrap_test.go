package steward_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/steward"
)

// The bare machine's guildhall_path, and the launcher beside it.
const (
	bareGuildhall = "/home/guild/.local/bin/guildhall"
	bareLauncher  = "/home/guild/.local/bin/guildhall-launcher"
)

// bootstrapFixture is the provisioning fixture on a workstation whose roster entry names guildhall_path and where
// nothing is there yet: sha256sum answers from the Fleet's files, and release install records what it installed.
type bootstrapFixture struct {
	*provisionFixture
	self      steward.SelfBinary
	mu        sync.Mutex
	installed []string
	// hashed counts the sha256sum runs of each path.
	hashed map[string]int
}

func newBootstrapFixture(t *testing.T) *bootstrapFixture {
	t.Helper()
	fx := &bootstrapFixture{provisionFixture: newProvisionFixture(t), hashed: map[string]int{}}
	roster := strings.Replace(collectSrc, `roles = ["steward", "director"]`, "roles = [\"steward\", \"director\"]\nguildhall_path = \""+bareGuildhall+"\"", 1)
	if err := os.WriteFile(filepath.Join(fx.checkout, "roster.toml"), []byte(roster), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.git(fx.checkout, "commit", "-q", "-am", "guildhall_path")
	fx.request = strings.Replace(fx.request, "herdr_session = \"guild\"\n", "herdr_session = \"guild\"\nbootstrap = true\n", 1)
	dir := t.TempDir()
	fx.self = steward.SelfBinary{Path: filepath.Join(dir, "guildhall"), Launcher: filepath.Join(dir, "launcher"), Version: "v0.9.0", GOOS: "linux", GOARCH: "amd64"}
	for path, data := range map[string]string{fx.self.Path: "guildhall v0.9.0 binary\n", fx.self.Launcher: "launcher binary\n"} {
		if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fx.f.Handle("guild-ws", fx.handle)
	return fx
}

func (fx *bootstrapFixture) handle(argv []string) ([]byte, bool, error) {
	switch {
	case argv[0] == "sha256sum":
		fx.mu.Lock()
		fx.hashed[argv[1]]++
		fx.mu.Unlock()
		data, ok := fx.f.Read(steward.Remote{Alias: "guild-ws", Path: argv[1]})
		if !ok {
			return nil, true, fmt.Errorf("sha256sum: %s: No such file or directory", argv[1])
		}
		return []byte(digest.Of(data).Hex() + "  " + argv[1] + "\n"), true, nil
	case len(argv) > 3 && argv[1] == "release" && argv[2] == "install":
		fx.mu.Lock()
		defer fx.mu.Unlock()
		fx.installed = append(fx.installed, strings.Join(argv, " "))
		return []byte("installed " + argv[3] + "\n"), true, nil
	}
	return fx.m.handle(argv)
}

func (fx *bootstrapFixture) setup() *steward.Setup {
	s := fx.provisionFixture.setup()
	s.Self = func() (steward.SelfBinary, error) { return fx.self, nil }
	return s
}

func (fx *bootstrapFixture) dryRun(t *testing.T, planDir string) *steward.SetupPlan {
	t.Helper()
	p, err := fx.setup().PlanProvision(context.Background(), fx.req(t), planDir)
	if err != nil {
		t.Fatalf("PlanProvision: %v", err)
	}
	if err := steward.WriteSetupPlan(p); err != nil {
		t.Fatalf("WriteSetupPlan: %v", err)
	}
	return p
}

// TestBootstrapDryRun: on a bare machine the dry run only runs sha256sum there, and plans copying the Steward's own
// binary to guildhall_path and the launcher beside it, then release install --from --sha256 of that file; twice, the
// same digest.
func TestBootstrapDryRun(t *testing.T) {
	fx := newBootstrapFixture(t)
	planDir := filepath.Join(t.TempDir(), "bootstrap-vds")
	p := fx.dryRun(t, planDir)
	if calls := fx.f.Calls(); !slices.Equal(calls, []string{"run guild-ws sha256sum " + bareGuildhall}) {
		t.Errorf("dry run calls %q, want only the sha256sum of guildhall_path", calls)
	}
	if got := stepIDs(p); !slices.Equal(got, []string{steward.StepPlaceGuildhall, steward.StepPlaceLauncher, steward.StepInstall}) {
		t.Errorf("steps %v", got)
	}
	b := p.Bootstrap
	if b == nil || b.Version != "v0.9.0" || b.Platform != "linux/amd64" || b.Guildhall != bareGuildhall || b.Launcher != bareLauncher {
		t.Fatalf("bootstrap %+v", b)
	}
	sum := digest.Of([]byte("guildhall v0.9.0 binary\n")).Hex()
	if len(p.Files) != 2 || p.Files[0].SHA256 != sum || p.Files[0].Target != bareGuildhall || p.Files[1].Target != bareLauncher {
		t.Errorf("files %+v", p.Files)
	}
	want := steward.InstallFromCommand(bareGuildhall, "v0.9.0", bareGuildhall, sum)
	if !slices.Equal(p.Steps[2].Argv, want) || steward.CheckCommand(want) != nil {
		t.Errorf("install argv %q, want %q allowed", p.Steps[2].Argv, want)
	}
	if p.RosterDiff != "" || len(p.Doctor) != 0 {
		t.Errorf("a bootstrap plan changes no roster and runs no doctor: diff %q, %d reports", p.RosterDiff, len(p.Doctor))
	}
	if again := fx.dryRun(t, planDir); again.Digest != p.Digest {
		t.Errorf("digest %s, then %s", p.Digest, again.Digest)
	}
}

// TestBootstrapApply: the apply copies both files, checks each with sha256sum before the binary first runs, installs
// it, and then the same request plans the ordinary provisioning; applying the finished plan again copies nothing.
func TestBootstrapApply(t *testing.T) {
	fx := newBootstrapFixture(t)
	planDir := filepath.Join(t.TempDir(), "bootstrap-vds")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	ctx := context.Background()
	out, err := s.ApplySetup(ctx, planDir, p.Digest)
	if err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply = %v, %v", out, err)
	}
	for _, f := range []struct{ path, want string }{{bareGuildhall, "guildhall v0.9.0 binary\n"}, {bareLauncher, "launcher binary\n"}} {
		if data, ok := fx.f.Read(steward.Remote{Alias: "guild-ws", Path: f.path}); !ok || string(data) != f.want {
			t.Errorf("%s = %q, %v", f.path, data, ok)
		}
	}
	if len(fx.installed) != 1 || !strings.Contains(fx.installed[0], "release install v0.9.0 --from "+bareGuildhall+" --sha256 "+p.Files[0].SHA256) {
		t.Errorf("installed %q", fx.installed)
	}
	// The binary was checked by sha256sum after it was copied and again before it ran.
	if fx.hashed[bareGuildhall] < 4 {
		t.Errorf("sha256sum ran %d times on %s", fx.hashed[bareGuildhall], bareGuildhall)
	}
	if rec := out.Log.Record(steward.StepInstall); rec == nil || !strings.Contains(rec.Note, "dry run again") {
		t.Errorf("install record %+v", rec)
	}
	fx.f.Calls()
	if out, err = s.ApplySetup(ctx, planDir, p.Digest); err != nil || out.State != steward.OutcomeDone {
		t.Fatalf("apply again = %v, %v", out, err)
	}
	if puts := slices.DeleteFunc(fx.f.Calls(), func(c string) bool { return !strings.HasPrefix(c, "put ") }); len(puts) != 0 {
		t.Errorf("finished plan applied again copied %q", puts)
	}
	next := fx.dryRun(t, filepath.Join(t.TempDir(), "provision-ops"))
	if next.Bootstrap != nil || !slices.Contains(stepIDs(next), "adopt") || next.Guildhall != bareGuildhall {
		t.Errorf("after the bootstrap the dry run plans %v with bootstrap %+v", stepIDs(next), next.Bootstrap)
	}
}

// TestBootstrapNeverRunsAnotherBinary: a file at guildhall_path that is not the plan's is never overwritten and never
// run: one there before the first apply changes the plan, and one changed after its copy stops the install.
func TestBootstrapNeverRunsAnotherBinary(t *testing.T) {
	fx := newBootstrapFixture(t)
	planDir := filepath.Join(t.TempDir(), "bootstrap-vds")
	p := fx.dryRun(t, planDir)
	s := fx.setup()
	ctx := context.Background()
	other := steward.Remote{Alias: "guild-ws", Path: bareGuildhall}
	fx.f.Write(other, []byte("someone else's guildhall\n"))
	if _, err := s.ApplySetup(ctx, planDir, p.Digest); !errors.Is(err, steward.ErrPlanChanged) {
		t.Fatalf("apply with a guildhall there = %v, want ErrPlanChanged", err)
	}
	if data, _ := fx.f.Read(other); string(data) != "someone else's guildhall\n" {
		t.Errorf("guildhall_path overwritten: %q", data)
	}

	fx = newBootstrapFixture(t)
	planDir = filepath.Join(t.TempDir(), "bootstrap-vds")
	p = fx.dryRun(t, planDir)
	s = fx.setup()
	s.Transport = &failSecondPut{Copier: fx.f}
	out, err := s.ApplySetup(ctx, planDir, p.Digest)
	if err == nil || out.Step != steward.StepPlaceLauncher {
		t.Fatalf("apply with the launcher's copy refused = %v, %v", out, err)
	}
	fx.f.Write(other, []byte("tampered\n"))
	out, err = s.ApplySetup(ctx, planDir, p.Digest)
	if err == nil || out.Step != steward.StepInstall || !strings.Contains(out.Diagnosis, "not run") {
		t.Fatalf("apply after tampering = %v, %v; want the install refused", out, err)
	}
	if len(fx.installed) != 0 {
		t.Errorf("installed %q", fx.installed)
	}
}

// failSecondPut is a Copier whose second put fails, as a refused scp does.
type failSecondPut struct {
	steward.Copier
	puts int
}

func (c *failSecondPut) Put(ctx context.Context, src string, dst steward.Remote) error {
	if c.puts++; c.puts == 2 {
		return errors.New("scp: connection refused")
	}
	return c.Copier.Put(ctx, src, dst)
}

// TestBootstrapDryRunRefuses: bootstrap needs guildhall_path, a Linux release of the Steward's own and the pinned
// version.
func TestBootstrapDryRunRefuses(t *testing.T) {
	for _, c := range []struct {
		name, want string
		change     func(fx *bootstrapFixture)
	}{
		{"no guildhall_path", "guildhall_path", func(fx *bootstrapFixture) {
			if err := os.WriteFile(filepath.Join(fx.checkout, "roster.toml"), []byte(collectSrc), 0o644); err != nil {
				fx.t.Fatal(err)
			}
			fx.git(fx.checkout, "commit", "-q", "-am", "no guildhall_path")
		}},
		{"another platform", "built for windows", func(fx *bootstrapFixture) { fx.self.GOOS = "windows" }},
		{"not a release", "not a release version", func(fx *bootstrapFixture) { fx.self.Version = "dev build" }},
		{"another pin", "pins Guildhall v0.9.0", func(fx *bootstrapFixture) { fx.self.Version = "v0.9.1" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newBootstrapFixture(t)
			c.change(fx)
			_, err := fx.setup().PlanProvision(context.Background(), fx.req(t), filepath.Join(t.TempDir(), "plan"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("PlanProvision: %v, want one naming %q", err, c.want)
			}
		})
	}
}
