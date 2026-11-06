package steward

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/digest"
)

// The steps of a bootstrap plan, which places Guildhall on a bare machine (docs/steward.md#bootstrapping-a-bare-machine).
const (
	StepPlaceGuildhall = "guildhall-copy"
	StepPlaceLauncher  = "launcher-copy"
	StepInstall        = "guildhall-install"
)

// The plan files of a bootstrap plan: the Steward's own binary and launcher, as the plan copies them.
const (
	bootstrapGuildhall = setupManagedDir + "/bootstrap/guildhall"
	bootstrapLauncher  = setupManagedDir + "/bootstrap/guildhall-launcher"
)

// SelfBinary is the Steward's own Guildhall release, which a bootstrap plan copies to a bare machine: the binary and
// the PATH launcher on this machine, the release version the binary reports and the platform it is built for.
type SelfBinary struct {
	Path, Launcher string
	Version        string
	GOOS, GOARCH   string
}

// Bootstrap is what a bootstrap plan places on the machine: release Version, built for Platform, at the roster's
// guildhall_path Guildhall, with the launcher beside it at Launcher, where release adopt later finds it.
type Bootstrap struct {
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	Guildhall string `json:"guildhall"`
	Launcher  string `json:"launcher"`
}

// bareMachine reports whether m has no file at its guildhall_path, as sha256sum there says; a bootstrap needs one.
func (s *Setup) bareMachine(ctx context.Context, m Machine) (bool, error) {
	if m.GuildhallPath == "" || !validDir(posixDirPattern, m.GuildhallPath) || path.Base(m.GuildhallPath) != DefaultGuildhall {
		return false, fmt.Errorf("bootstrap needs machine %q's guildhall_path in roster.toml: the absolute path of a file named guildhall, in a directory that exists and that SSH commands find on PATH, such as /home/<user>/.local/bin/guildhall", m.Label)
	}
	_, err := s.remoteHash(ctx, m.SSHAlias, m.GuildhallPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("checking for Guildhall at %s:%s: %w", m.Label, m.GuildhallPath, err)
	}
	return false, nil
}

// remoteHash is the sha256 sha256sum prints for the file at file on the machine with alias; an error wrapping
// fs.ErrNotExist when sha256sum says there is none.
func (s *Setup) remoteHash(ctx context.Context, alias, file string) (string, error) {
	out, err := s.Runner.Run(ctx, alias, HashCommand(file))
	if err != nil {
		if ctx.Err() == nil && missingSource(err.Error()) {
			return "", fmt.Errorf("%w: %w", fs.ErrNotExist, err)
		}
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !hexPattern.MatchString(fields[0]) {
		return "", fmt.Errorf("sha256sum %s printed %q, not a sha256", file, firstLine(string(out), 200))
	}
	return fields[0], nil
}

// planBootstrap is the provisioning plan for a bare machine: it copies the Steward's own release binary to the
// machine's guildhall_path and the launcher beside it, checks each with sha256sum before it first runs, and installs
// the binary with release install --from --sha256. It plans nothing else, since the doctor that plans the copy needs
// Guildhall there; once it is done, the same request's dry run plans the rest.
func (s *Setup) planBootstrap(m Machine, req *ProvisionRequest, entry RosterEntry, rosterData []byte, planDir string) (*SetupPlan, error) {
	if s.Self == nil {
		return nil, errors.New("this Steward cannot name its own binary to bootstrap the machine with")
	}
	self, err := s.Self()
	if err != nil {
		return nil, fmt.Errorf("bootstrap: the Steward's own binary: %w", err)
	}
	if self.GOOS != string(m.OS) {
		return nil, fmt.Errorf("bootstrap: the Steward's own binary is built for %s/%s, and machine %q is %s", self.GOOS, self.GOARCH, m.Label, m.OS)
	}
	if !versionPattern.MatchString(self.Version) {
		return nil, fmt.Errorf("bootstrap: the Steward's own binary reports version %q, not a release version", self.Version)
	}
	if pin := req.Pins.Guildhall; pin != "" && pin != self.Version {
		return nil, fmt.Errorf("bootstrap: the request pins Guildhall %s, and the Steward's own binary, which the bootstrap places, is %s; pin %s or run the Steward on %s", pin, self.Version, self.Version, pin)
	}
	b := &Bootstrap{Version: self.Version, Platform: self.GOOS + "/" + self.GOARCH, Guildhall: m.GuildhallPath,
		Launcher: path.Dir(m.GuildhallPath) + "/guildhall-launcher"}
	p := &SetupPlan{Schema: SetupPlanSchema, Kind: SetupKindProvision, Machine: m.Label, Director: req.Director, CopyDir: req.CopyDir,
		SSHAlias: m.SSHAlias, Guildhall: m.Guildhall(), Steward: s.Steward, PlanDir: planDir, SetupDir: req.SetupDir,
		RosterDigest: digest.Of(rosterData).Hex(), Roster: entry, Request: req, Bootstrap: b, files: map[string][]byte{}}
	for _, f := range []struct{ name, src, target string }{{bootstrapGuildhall, self.Path, b.Guildhall}, {bootstrapLauncher, self.Launcher, b.Launcher}} {
		data, err := os.ReadFile(f.src)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: %w", err)
		}
		p.files[f.name] = data
		p.Files = append(p.Files, PlannedFile{Name: f.name, Target: f.target, SHA256: digest.Of(data).Hex()})
	}
	sum := p.Files[0].SHA256
	p.Steps = []SetupStep{
		{ID: StepPlaceGuildhall, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(bootstrapGuildhall)), Remote{m.SSHAlias, b.Guildhall}.String()),
			Expect: "the Steward's own guildhall " + b.Version + " (" + b.Platform + ") is copied to " + b.Guildhall + ", where nothing is, and sha256sum there reports " + sum},
		{ID: StepPlaceLauncher, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(bootstrapLauncher)), Remote{m.SSHAlias, b.Launcher}.String()),
			Expect: "the launcher is copied beside it to " + b.Launcher + ", where release adopt finds it, and sha256sum there reports " + p.Files[1].SHA256},
		{ID: StepInstall, Kind: StepKindRemote, Argv: InstallFromCommand(b.Guildhall, b.Version, b.Guildhall, sum),
			Expect: "sha256sum still reports " + sum + " for " + b.Guildhall + " before it first runs, and release install makes it release " + b.Version + " in the machine's Guildhall home; then the same request's dry run plans the copy"},
	}
	p.Digest = p.digest()
	return p, nil
}

// runPlace copies a bootstrap file to the machine, executable, unless sha256sum there already reports its sha256,
// and verifies the copy with sha256sum. Another file at the target, even one a copy stopped part way left, is refused
// with ErrCopyExists and left as it is.
func (s *Setup) runPlace(ctx context.Context, p *SetupPlan, f PlannedFile) stepResult {
	got, err := s.remoteHash(ctx, p.SSHAlias, f.Target)
	switch {
	case err == nil && got == f.SHA256:
		return stepResult{note: "already in place with sha256 " + f.SHA256}
	case err == nil:
		return failed("%v: %s:%s exists with sha256 %s, not the plan's %s; it is left as it is (if a copy by this step stopped part way, remove it on the machine and apply again)", ErrCopyExists, p.Machine, f.Target, got, f.SHA256)
	case !errors.Is(err, fs.ErrNotExist):
		return failed("checking %s:%s before copying, to be sure nothing is there: %v; nothing was copied, so apply again once it can be read", p.Machine, f.Target, err)
	}
	dir, err := os.MkdirTemp("", "guildhall-bootstrap-")
	if err != nil {
		return failed("%v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	local := filepath.Join(dir, path.Base(f.Target))
	// The mode travels with scp, so the new file on the machine is executable.
	if err := os.WriteFile(local, p.files[f.Name], 0o755); err != nil {
		return failed("%v", err)
	}
	target := Remote{p.SSHAlias, f.Target}
	if err := s.Transport.Put(ctx, local, target); err != nil {
		return failed("copying %s to %s: %v; if the directory does not exist, make it on the machine (mkdir -p %s) and apply again", path.Base(f.Target), target, err, path.Dir(f.Target))
	}
	if got, err = s.remoteHash(ctx, p.SSHAlias, f.Target); err != nil || got != f.SHA256 {
		return failed("after copying, sha256sum of %s reports %q (%v), not the plan's %s; remove it on the machine and apply again", target, got, err, f.SHA256)
	}
	return stepResult{note: "copied, and sha256sum reports " + f.SHA256}
}

// runInstall checks both bootstrap files with sha256sum again, so the binary never runs unless it is the plan's,
// then installs it with release install --from --sha256, which checks its sha256 once more itself.
func (s *Setup) runInstall(ctx context.Context, p *SetupPlan, st SetupStep) stepResult {
	for _, f := range p.Files {
		got, err := s.remoteHash(ctx, p.SSHAlias, f.Target)
		if err != nil || got != f.SHA256 {
			return failed("sha256sum of %s:%s reports %q (%v), not the plan's %s, so it is not run; make a new dry run", p.Machine, f.Target, got, err, f.SHA256)
		}
	}
	output, err := s.run(ctx, p, st.Argv)
	if err != nil {
		return failed("release install: %v; a binary that cannot run there (\"exec format error\") is not built for the machine, which must be %s", err, p.Bootstrap.Platform)
	}
	return stepResult{output: output, note: fmt.Sprintf("Guildhall %s is installed on %s; make the provisioning dry run again with the same request to plan the copy", p.Bootstrap.Version, p.Machine)}
}
