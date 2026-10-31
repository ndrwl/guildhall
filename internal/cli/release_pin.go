package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// releaseCopy selects the copy a pin command acts on, --copy or copy discovery, and its store, which must exist.
func (a *App) releaseCopy(cp *copyFlags) (dir, storePath string, err error) {
	if err := cp.apply(a, &storePath, nil, nil); err != nil {
		return "", "", err
	}
	if cp.dir == "" {
		return "", "", cp.missing(a)
	}
	if dir, err = filepath.Abs(cp.dir); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
		return "", "", noStore(storePath)
	}
	return dir, storePath, nil
}

// pinChange is what setPin did: the copy's newest pin event and whether this run recorded it.
type pinChange struct {
	Pin     store.ReleasePin
	Changed bool
}

// setPin pins the copy in dir, whose store is at storePath, to the installed, intact release r, from source: under
// the home lock, it registers the copy in the inventory, records a release-pin event unless the newest one already
// names r, and then writes the copy's release.pin cache by rename, so a pin file always follows its event
// (docs/releases.md#the-pin). The lock serializes pin changes with each other and, from later slices, with task
// creation and pruning.
func (a *App) setPin(h release.Home, dir, storePath string, r release.Release, source store.ReleasePinSource) (pinChange, error) {
	unlock, err := h.Lock(releaseLockWait)
	if err != nil {
		return pinChange{}, err
	}
	defer func() { _ = unlock() }()
	if err := h.RegisterLocked(dir); err != nil {
		return pinChange{}, fmt.Errorf("register copy %s in %s: %w", dir, h.CopiesFile(), err)
	}
	ch, err := withStoreClock(storePath, false, a.Now, func(s *store.Store) (pinChange, error) {
		latest, err := s.LatestReleasePin()
		switch {
		case err == nil && latest.Version == r.Version && latest.SHA256 == r.SHA256:
			return pinChange{Pin: latest}, nil
		case err != nil && !errors.Is(err, store.ErrNotFound):
			return pinChange{}, err
		}
		p, _, err := s.RecordReleasePin(store.ReleasePin{Version: r.Version, SHA256: r.SHA256, Source: source})
		return pinChange{Pin: p, Changed: true}, err
	})
	if err != nil {
		return pinChange{}, err
	}
	return ch, release.WritePin(dir, r.Version)
}

// releaseUse runs `guildhall release use <version> [--copy <dir>]`: a standalone copy's pin moves to an installed,
// intact release (docs/contracts/cli.md#release).
func (a *App) releaseUse(args []string) int {
	fl := flag.NewFlagSet("release use", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release use: " + err.Error())
	}
	if fl.NArg() != 1 {
		return a.usageError("release use: want one version\n" + releaseUsage)
	}
	version := fl.Arg(0)
	dir, storePath, err := a.releaseCopy(&cp)
	if err != nil {
		return a.fail(fmt.Errorf("release use: %w", err))
	}
	if _, err := os.Lstat(filepath.Join(dir, managedFile)); err == nil {
		return a.fail(fmt.Errorf("release use: copy %s is managed: its release is a managed key, release and release_sha256 in %s, which the Steward changes by rollout and guildhall config reload --copy applies", dir, managedFile))
	}
	h, err := a.releaseHome()
	if err != nil {
		return a.fail(fmt.Errorf("release use: %w", err))
	}
	if err := release.ValidVersion(version); err != nil {
		return a.fail(fmt.Errorf("release use: %w", err))
	}
	r, err := h.Runnable(version)
	if err != nil {
		return a.fail(fmt.Errorf("release use: %w; install it first with guildhall release install %s --from <file> --sha256 <hex>", err, version))
	}
	ch, err := a.setPin(h, dir, storePath, r, store.PinUse)
	if err != nil {
		return a.fail(fmt.Errorf("release use: %w", err))
	}
	return a.printPin(dir, ch)
}

// printPin prints a pin change, or that there was none.
func (a *App) printPin(dir string, ch pinChange) int {
	p := ch.Pin
	var line string
	switch {
	case !ch.Changed:
		line = fmt.Sprintf("copy %s already pins %s (%s); nothing changed\n", dir, p.Version, shortSHA(p.SHA256))
	case p.PreviousVersion == "":
		line = fmt.Sprintf("copy %s pins %s (%s), its first pin; new work runs on it, and guildhall director reload moves a running Director onto it\n", dir, p.Version, shortSHA(p.SHA256))
	default:
		line = fmt.Sprintf("copy %s pins %s (%s), was %s; new work runs on it, and guildhall director reload moves a running Director onto it\n", dir, p.Version, shortSHA(p.SHA256), p.PreviousVersion)
	}
	if _, err := io.WriteString(a.Stdout, line); err != nil {
		return 1
	}
	return 0
}

// probeLauncher refuses a file that is not a PATH launcher: run with LauncherProbeEnv, a launcher prints its
// report and dispatches nothing. App.ProbeLauncher replaces it in tests.
func (a *App) probeLauncher(ctx context.Context, path string) error {
	if a.ProbeLauncher != nil {
		return a.ProbeLauncher(ctx, path)
	}
	ctx, cancel := context.WithTimeout(ctx, binaryPreflightTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = append(os.Environ(), release.LauncherProbeEnv+"=1")
	out, err := cmd.Output()
	if err != nil {
		return errors.New(refusalReason("run", err, out))
	}
	var r release.LauncherReport
	if err := json.Unmarshal(out, &r); err != nil || r.Schema != release.LauncherSchema || r.Version != release.LauncherVersion {
		return fmt.Errorf("%s is not a guildhall launcher: it printed no %s report", path, release.LauncherSchema)
	}
	return nil
}

// findLauncher is the launcher program to install: from when given, else guildhall-launcher beside near, the
// guildhall it replaces, else guildhall-launcher on PATH, as `go install …/cmd/guildhall-launcher@<version>`
// places it.
func (a *App) findLauncher(from, near string) (string, error) {
	if from != "" {
		return filepath.Abs(from)
	}
	if near != "" {
		beside := filepath.Join(filepath.Dir(near), release.LauncherName(runtime.GOOS))
		if info, err := os.Stat(beside); err == nil && info.Mode().IsRegular() {
			return beside, nil
		}
	}
	p, err := a.LookPath(release.LauncherName(runtime.GOOS))
	if err != nil {
		return "", fmt.Errorf("no %s found beside %s or on PATH: build it with go install github.com/ndrwl/guildhall/cmd/guildhall-launcher@<version>, or name it with --launcher <file>", release.LauncherName(runtime.GOOS), near)
	}
	return filepath.Abs(p)
}

// placeFile puts a copy of src at dst, replacing whatever is there by rename: the copy is written beside dst
// first, then an existing dst is moved aside to dst.old, which a running Windows executable allows where
// replacing it does not, and the copy renamed into place. The moved-aside file is removed when it can be.
func placeFile(src, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".guildhall-launcher-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	defer func() { _ = os.Remove(tmpPath) }()
	sum, err := copyExecutable(src, tmpPath)
	if err != nil {
		return "", err
	}
	old := dst + ".old"
	moved := false
	if _, err := os.Lstat(dst); err == nil {
		_ = os.Remove(old)
		if err := os.Rename(dst, old); err != nil {
			return "", err
		}
		moved = true
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		if moved {
			_ = os.Rename(old, dst)
		}
		return "", err
	}
	if moved {
		_ = os.Remove(old)
	}
	return sum, nil
}

// copyExecutable copies src to a new executable file at dst, synced, and returns its sha256.
func copyExecutable(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return "", err
	}
	_, cerr := io.Copy(out, in)
	if err := errors.Join(cerr, out.Sync(), out.Close()); err != nil {
		return "", err
	}
	return fileSHA256(dst)
}

// installLauncher probes the launcher program src and installs it as the home's bin/guildhall.
func (a *App) installLauncher(h release.Home, src string) (string, error) {
	if err := a.probeLauncher(context.Background(), src); err != nil {
		return "", err
	}
	return placeFile(src, h.Launcher())
}

// releaseInstallLauncher runs `guildhall release install --launcher [--from <file>]`: it replaces the home's PATH
// launcher, bin/guildhall, and the guildhall on PATH too when that is a copy of the launcher it replaces, as adopt
// leaves one.
func (a *App) releaseInstallLauncher(from string) int {
	h, err := a.releaseHome()
	if err != nil {
		return a.fail(fmt.Errorf("release install --launcher: %w", err))
	}
	src, err := a.findLauncher(from, "")
	if err != nil {
		return a.fail(fmt.Errorf("release install --launcher: %w", err))
	}
	oldSum, _ := fileSHA256(h.Launcher())
	sum, err := a.installLauncher(h, src)
	if err != nil {
		return a.fail(fmt.Errorf("release install --launcher: %w", err))
	}
	lines := []string{fmt.Sprintf("installed the launcher %s (%s) at %s", src, shortSHA(sum), h.Launcher())}
	if bin, err := a.lookPathBinary(); err == nil && oldSum != "" && bin.SHA256 == oldSum && !samePath(bin.Path, h.Launcher()) {
		if _, err := placeFile(h.Launcher(), bin.Path); err != nil {
			return a.fail(fmt.Errorf("release install --launcher: replace the launcher on PATH %s: %w", bin.Path, err))
		}
		lines = append(lines, "replaced the launcher on PATH at "+bin.Path)
	}
	if _, err := io.WriteString(a.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return 1
	}
	return 0
}

// backupName is where adopt keeps the guildhall it replaces: guildhall.pre-adopt, or guildhall.pre-adopt.exe on
// Windows, beside it.
func backupName(path string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(filepath.Dir(path), "guildhall.pre-adopt.exe")
	}
	return filepath.Join(filepath.Dir(path), "guildhall.pre-adopt")
}

// releaseAdopt runs `guildhall release adopt [--copy <dir>] [--launcher <file>]` (docs/contracts/cli.md#release):
// the guildhall on PATH becomes the copy's first release and the launcher takes its place on PATH. A second run
// reports what is already in place.
func (a *App) releaseAdopt(args []string) int {
	fl := flag.NewFlagSet("release adopt", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	var cp copyFlags
	cp.register(fl)
	launcherFrom := fl.String("launcher", "", "the launcher program to install")
	if err := parseInterspersed(fl, args); err != nil {
		return a.usageError("release adopt: " + err.Error())
	}
	if fl.NArg() != 0 {
		return a.usageError(fmt.Sprintf("release adopt: unexpected argument %q\n%s", fl.Arg(0), releaseUsage))
	}
	fail := func(err error) int { return a.fail(fmt.Errorf("release adopt: %w", err)) }
	dir, storePath, err := a.releaseCopy(&cp)
	if err != nil {
		return fail(err)
	}
	h, err := a.releaseHome()
	if err != nil {
		return fail(err)
	}
	bin, err := a.lookPathBinary()
	if err != nil {
		return fail(fmt.Errorf("guildhall on PATH: %w", err))
	}
	var lines []string
	launcherSum, _ := fileSHA256(h.Launcher())
	adopted := launcherSum != "" && bin.SHA256 == launcherSum
	// The release this copy adopts: the guildhall on PATH before the machine adopted, else, on a machine whose
	// PATH already runs the launcher, this running release.
	var r release.Release
	if adopted {
		lines = append(lines, fmt.Sprintf("the guildhall on PATH %s is already the launcher (%s)", bin.Path, shortSHA(launcherSum)))
	} else {
		report, err := a.releaseProbe(context.Background(), bin.Path)
		if err != nil {
			return fail(fmt.Errorf("%s: version --json: %w", bin.Path, err))
		}
		res, err := h.Install(context.Background(), report.Version, bin.Path, bin.SHA256, a.releaseProbe, a.now(), releaseLockWait)
		if err != nil {
			return fail(err)
		}
		r = res.Release
		if res.Already {
			lines = append(lines, fmt.Sprintf("%s is already installed at %s", r.Version, h.ReleaseDir(r.Version)))
		} else {
			lines = append(lines, fmt.Sprintf("installed the guildhall on PATH %s as %s (%s) at %s", bin.Path, r.Version, shortSHA(r.SHA256), h.ReleaseDir(r.Version)))
		}
	}
	// The launcher is found, probed and installed into the home before the copy is touched, so an adopt refused
	// for its launcher leaves the copy unpinned.
	var launcherLine string
	if !adopted {
		src, err := a.findLauncher(*launcherFrom, bin.Path)
		if err != nil {
			return fail(err)
		}
		if launcherSum, err = a.installLauncher(h, src); err != nil {
			return fail(fmt.Errorf("launcher %s: %w", src, err))
		}
		launcherLine = fmt.Sprintf("installed the launcher %s (%s) at %s", src, shortSHA(launcherSum), h.Launcher())
	}
	if pin, err := release.ReadPin(dir); err == nil {
		// An adopted copy keeps its pin; only a copy with none gets the adopted release as its first.
		if err := h.Register(dir, releaseLockWait); err != nil {
			return fail(err)
		}
		lines = append(lines, fmt.Sprintf("copy %s already pins %s", dir, pin))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	} else {
		if adopted {
			// The launcher ran this release for the copy with no pin by recovery dispatch: it is installed.
			if r, err = h.Runnable(Version()); err != nil {
				return fail(fmt.Errorf("this running guildhall: %w", err))
			}
		}
		ch, err := a.setPin(h, dir, storePath, r, store.PinAdopt)
		if err != nil {
			return fail(err)
		}
		lines = append(lines, fmt.Sprintf("copy %s pins %s (%s) and is registered in %s", dir, ch.Pin.Version, shortSHA(ch.Pin.SHA256), h.CopiesFile()))
	}
	if !adopted {
		lines = append(lines, launcherLine)
		if err := a.replaceWithLauncher(h, bin.Path); err != nil {
			return fail(err)
		}
		lines = append(lines, fmt.Sprintf("replaced %s with the launcher, keeping the old binary as %s", bin.Path, backupName(bin.Path)))
	}
	if _, err := io.WriteString(a.Stdout, strings.Join(lines, "\n")+"\n"); err != nil {
		return 1
	}
	return 0
}

// replaceWithLauncher replaces the guildhall at path, on PATH, with a copy of the home's launcher by rename,
// keeping the old binary beside it (backupName). On Windows, where a running executable cannot be replaced, it
// refuses while another Guildhall process runs from path, naming it.
func (a *App) replaceWithLauncher(h release.Home, path string) error {
	backup := backupName(path)
	if _, err := os.Lstat(backup); err == nil {
		return fmt.Errorf("%s already exists; move it away before adopting again", backup)
	}
	if pids, err := release.RunningFrom(path, os.Getpid()); err != nil {
		return fmt.Errorf("check for Guildhall processes running %s: %w", path, err)
	} else if len(pids) > 0 {
		return fmt.Errorf("guildhall processes %v run from %s, which cannot be replaced while they run; stop them (guildhall director stop) and adopt again", pids, path)
	}
	tmp := filepath.Join(filepath.Dir(path), ".guildhall-adopt-"+filepath.Base(path))
	_ = os.Remove(tmp)
	defer func() { _ = os.Remove(tmp) }()
	if _, err := copyExecutable(h.Launcher(), tmp); err != nil {
		return err
	}
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	return nil
}
