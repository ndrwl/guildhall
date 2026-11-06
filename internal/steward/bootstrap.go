package steward

import (
	"context"
	"debug/buildinfo"
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

// bareMachine reports whether m has no file at its guildhall_path, as sha256sum (certutil on Windows) there says; a
// bootstrap needs one.
func (s *Setup) bareMachine(ctx context.Context, m Machine) (bool, error) {
	if m.GuildhallPath == "" || !validPath(m.OS, m.GuildhallPath) || path.Base(m.GuildhallPath) != exeName(m.OS, DefaultGuildhall) {
		example := "/home/<user>/.local/bin/guildhall"
		if m.OS == OSWindows {
			example = "C:/Users/<user>/.local/bin/guildhall.exe"
		}
		return false, fmt.Errorf("bootstrap needs machine %q's guildhall_path in roster.toml: the absolute path of a file named %s, in a directory that exists and that SSH commands find on PATH, such as %s", m.Label, exeName(m.OS, DefaultGuildhall), example)
	}
	_, err := s.remoteHash(ctx, m.OS, m.SSHAlias, m.GuildhallPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("checking for Guildhall at %s:%s: %w", m.Label, m.GuildhallPath, err)
	}
	return false, nil
}

// remoteHash is the sha256 HashCommand prints for the file at file on the goos machine with alias; an error wrapping
// fs.ErrNotExist when it says there is none.
func (s *Setup) remoteHash(ctx context.Context, goos OS, alias, file string) (string, error) {
	argv := HashCommand(goos, file)
	out, err := s.Runner.Run(ctx, alias, argv)
	if err != nil {
		if ctx.Err() == nil && hashMissing(err.Error()) {
			return "", fmt.Errorf("%w: %w", fs.ErrNotExist, err)
		}
		return "", err
	}
	sum, ok := ParseHash(goos, out)
	if !ok {
		return "", fmt.Errorf("%s %s printed %q, not a sha256", argv[0], file, firstLine(string(out), 200))
	}
	return sum, nil
}

// bootstrapBinary is the Guildhall release a bootstrap places on m: the cross-built release in the request's
// bootstrap_from, read without running it, or else the Steward's own binary. It must be built for m's OS, report a
// release version and, when the request pins Guildhall, be that version.
func (s *Setup) bootstrapBinary(m Machine, req *ProvisionRequest) (SelfBinary, error) {
	var bin SelfBinary
	what := "the Steward's own binary"
	if req.BootstrapFrom != "" {
		what = "the release in bootstrap_from"
		read := s.ReadBuild
		if read == nil {
			read = ReadBuild
		}
		var err error
		if bin, err = crossBuilt(read, req.BootstrapFrom, m.OS); err != nil {
			return SelfBinary{}, fmt.Errorf("bootstrap: %w", err)
		}
	} else {
		if s.Self == nil {
			return SelfBinary{}, errors.New("this Steward cannot name its own binary to bootstrap the machine with")
		}
		var err error
		if bin, err = s.Self(); err != nil {
			return SelfBinary{}, fmt.Errorf("bootstrap: the Steward's own binary: %w", err)
		}
	}
	switch {
	case bin.GOOS != string(m.OS) && req.BootstrapFrom == "":
		return SelfBinary{}, fmt.Errorf("bootstrap: the Steward's own binary is built for %s/%s, and machine %q is %s; name a release built for it with bootstrap_from in the request", bin.GOOS, bin.GOARCH, m.Label, m.OS)
	case bin.GOOS != string(m.OS):
		return SelfBinary{}, fmt.Errorf("bootstrap: %s is built for %s/%s, and machine %q is %s", what, bin.GOOS, bin.GOARCH, m.Label, m.OS)
	case !versionPattern.MatchString(bin.Version):
		return SelfBinary{}, fmt.Errorf("bootstrap: %s reports version %q, not a release version", what, bin.Version)
	}
	if pin := req.Pins.Guildhall; pin != "" && pin != bin.Version {
		return SelfBinary{}, fmt.Errorf("bootstrap: the request pins Guildhall %s, and %s, which the bootstrap places, is %s; pin %s or place %s", pin, what, bin.Version, bin.Version, pin)
	}
	return bin, nil
}

// planBootstrap is the provisioning plan for a bare machine: it copies a Guildhall release binary, the Steward's own
// or the cross-built one the request names, to the machine's guildhall_path and the launcher beside it, checks each
// with sha256sum (certutil on Windows) before it first runs, and installs the binary with release install --from
// --sha256. It plans nothing else, since the doctor that plans the copy needs Guildhall there; once it is done, the
// same request's dry run plans the rest.
func (s *Setup) planBootstrap(m Machine, req *ProvisionRequest, entry RosterEntry, rosterData []byte, planDir string) (*SetupPlan, error) {
	self, err := s.bootstrapBinary(m, req)
	if err != nil {
		return nil, err
	}
	b := &Bootstrap{Version: self.Version, Platform: self.GOOS + "/" + self.GOARCH, Guildhall: m.GuildhallPath,
		Launcher: path.Dir(m.GuildhallPath) + "/" + exeName(m.OS, "guildhall-launcher")}
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
	whose, hash := "the Steward's own", HashCommand(m.OS, "")[0]
	if req.BootstrapFrom != "" {
		whose = "the cross-built"
	}
	p.Steps = []SetupStep{
		{ID: StepPlaceGuildhall, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(bootstrapGuildhall)), Remote{m.SSHAlias, b.Guildhall}.String()),
			Expect: whose + " guildhall " + b.Version + " (" + b.Platform + ") is copied to " + b.Guildhall + ", where nothing is, and " + hash + " there reports " + sum},
		{ID: StepPlaceLauncher, Kind: StepKindCopy, Argv: scpCommand(filepath.Join(planDir, filepath.FromSlash(bootstrapLauncher)), Remote{m.SSHAlias, b.Launcher}.String()),
			Expect: "the launcher is copied beside it to " + b.Launcher + ", where release adopt finds it, and " + hash + " there reports " + p.Files[1].SHA256},
		{ID: StepInstall, Kind: StepKindRemote, Argv: InstallFromCommand(b.Guildhall, b.Version, b.Guildhall, sum),
			Expect: hash + " still reports " + sum + " for " + b.Guildhall + " before it first runs, and release install makes it release " + b.Version + " in the machine's Guildhall home; then the same request's dry run plans the copy"},
	}
	p.Digest = p.digest()
	return p, nil
}

// runPlace copies a bootstrap file to the machine, executable, unless sha256sum there already reports its sha256,
// and verifies the copy with sha256sum. Another file at the target, even one a copy stopped part way left, is refused
// with ErrCopyExists and left as it is.
func (s *Setup) runPlace(ctx context.Context, p *SetupPlan, f PlannedFile) stepResult {
	got, err := s.remoteHash(ctx, p.Roster.OS, p.SSHAlias, f.Target)
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
		mkdir := "mkdir -p " + path.Dir(f.Target)
		if p.Roster.OS == OSWindows {
			mkdir = "New-Item -ItemType Directory -Force " + path.Dir(f.Target)
		}
		return failed("copying %s to %s: %v; if the directory does not exist, make it on the machine (%s) and apply again", path.Base(f.Target), target, err, mkdir)
	}
	hash := HashCommand(p.Roster.OS, "")[0]
	if got, err = s.remoteHash(ctx, p.Roster.OS, p.SSHAlias, f.Target); err != nil || got != f.SHA256 {
		return failed("after copying, %s of %s reports %q (%v), not the plan's %s; remove it on the machine and apply again", hash, target, got, err, f.SHA256)
	}
	return stepResult{note: "copied, and " + hash + " reports " + f.SHA256}
}

// runInstall checks both bootstrap files with sha256sum again, so the binary never runs unless it is the plan's,
// then installs it with release install --from --sha256, which checks its sha256 once more itself.
func (s *Setup) runInstall(ctx context.Context, p *SetupPlan, st SetupStep) stepResult {
	for _, f := range p.Files {
		got, err := s.remoteHash(ctx, p.Roster.OS, p.SSHAlias, f.Target)
		if err != nil || got != f.SHA256 {
			return failed("%s of %s:%s reports %q (%v), not the plan's %s, so it is not run; make a new dry run", HashCommand(p.Roster.OS, "")[0], p.Machine, f.Target, got, err, f.SHA256)
		}
	}
	output, err := s.run(ctx, p, st.Argv)
	if err != nil {
		return failed("release install: %v; a binary that cannot run there (\"exec format error\") is not built for the machine, which must be %s", err, p.Bootstrap.Platform)
	}
	return stepResult{output: output, note: fmt.Sprintf("Guildhall %s is installed on %s; make the provisioning dry run again with the same request to plan the copy", p.Bootstrap.Version, p.Machine)}
}

// The main packages of Guildhall's two programs, as a binary's build information names them.
const (
	guildhallPackage = "github.com/ndrwl/guildhall/cmd/guildhall"
	launcherPackage  = "github.com/ndrwl/guildhall/cmd/guildhall-launcher"
	// versionFlag is how a release build sets its version: -ldflags "-X <versionFlag><tag>".
	versionFlag = "github.com/ndrwl/guildhall/internal/cli.version="
)

// BuildInfo is what a Go binary records of its own build, read without running it, so a binary built for another
// operating system can be checked: its main package, release version, platform and whether its source was modified.
type BuildInfo struct {
	Package, Version, GOOS, GOARCH string
	Modified                       bool
}

// ReadBuild reads the build information of the Go binary at file, of any platform, without running it. The version
// is the one a release build sets with -ldflags -X, else the module version go install records.
func ReadBuild(file string) (BuildInfo, error) {
	info, err := buildinfo.ReadFile(file)
	if err != nil {
		return BuildInfo{}, err
	}
	b := BuildInfo{Package: info.Path, Version: info.Main.Version}
	if b.Version == "(devel)" {
		b.Version = ""
	}
	for _, kv := range info.Settings {
		switch kv.Key {
		case "GOOS":
			b.GOOS = kv.Value
		case "GOARCH":
			b.GOARCH = kv.Value
		case "vcs.modified":
			b.Modified = kv.Value == "true"
		case "-ldflags":
			if _, v, ok := strings.Cut(kv.Value, versionFlag); ok {
				b.Version, _, _ = strings.Cut(v, " ")
				b.Version = strings.Trim(b.Version, `'"`)
			}
		}
	}
	return b, nil
}

// crossBuilt is the Guildhall release in dir built for goos, such as one `GOOS=windows go install` put in
// $(go env GOPATH)/bin/windows_amd64: guildhall and guildhall-launcher (.exe on Windows), each read with read and
// refused unless it is that program, unmodified, built for one platform; the binary must report a version.
func crossBuilt(read func(string) (BuildInfo, error), dir string, goos OS) (SelfBinary, error) {
	bin := SelfBinary{Path: filepath.Join(dir, exeName(goos, DefaultGuildhall)), Launcher: filepath.Join(dir, exeName(goos, "guildhall-launcher"))}
	var infos [2]BuildInfo
	for i, f := range []struct{ file, pkg string }{{bin.Path, guildhallPackage}, {bin.Launcher, launcherPackage}} {
		b, err := read(f.file)
		switch {
		case err != nil:
			return SelfBinary{}, fmt.Errorf("bootstrap_from: %s: %w", f.file, err)
		case b.Package != f.pkg:
			return SelfBinary{}, fmt.Errorf("bootstrap_from: %s is built from %q, not %s", f.file, b.Package, f.pkg)
		case b.Modified:
			return SelfBinary{}, fmt.Errorf("bootstrap_from: %s is built from modified source, so it is not a release", f.file)
		}
		infos[i] = b
	}
	if infos[0].GOOS != infos[1].GOOS || infos[0].GOARCH != infos[1].GOARCH {
		return SelfBinary{}, fmt.Errorf("bootstrap_from: guildhall is built for %s/%s and the launcher for %s/%s", infos[0].GOOS, infos[0].GOARCH, infos[1].GOOS, infos[1].GOARCH)
	}
	bin.Version, bin.GOOS, bin.GOARCH = infos[0].Version, infos[0].GOOS, infos[0].GOARCH
	return bin, nil
}
