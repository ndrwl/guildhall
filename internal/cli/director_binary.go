package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ndrwl/guildhall/internal/launcher"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
)

// binaryPreflightTimeout bounds the runs of the guildhall binary on PATH a supervisor checks before it moves onto it.
const binaryPreflightTimeout = 2 * time.Minute

// imageSHA256 is the lowercase hex sha256 of the binary image this process runs: /proc/self/exe on Linux, which
// still opens the running image after an install renamed another file into its place, and the executable's path
// elsewhere.
func (a *App) imageSHA256() (string, error) {
	path := "/proc/self/exe"
	if runtime.GOOS != "linux" {
		exe := a.Executable
		if exe == nil {
			exe = os.Executable
		}
		var err error
		if path, err = exe(); err != nil {
			return "", err
		}
	}
	return fileSHA256(path)
}

// fileSHA256 is the lowercase hex sha256 of the file at path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pathBinary is the guildhall binary on PATH: its absolute path and sha256, either "" when it could not be found
// or hashed.
type pathBinary struct {
	Path   string
	SHA256 string
}

// lookPathBinary resolves guildhall on PATH and hashes it.
func (a *App) lookPathBinary() (pathBinary, error) {
	path, err := a.LookPath("guildhall")
	if err != nil {
		return pathBinary{}, err
	}
	if path, err = filepath.Abs(path); err != nil {
		return pathBinary{}, err
	}
	sum, err := fileSHA256(path)
	return pathBinary{Path: path, SHA256: sum}, err
}

// targetBinary is the binary a Director of the copy in copyDir runs on: on a copy that has adopted releases, one
// with a release.pin, the pinned release, which must be installed and intact, with pinned naming its version; on
// any other copy the guildhall on PATH, as before releases (docs/releases.md#how-a-running-director-picks-up-a-new-release).
// The PATH launcher of an adopted copy runs the pin too, so comparing with it would compare with the launcher.
func (a *App) targetBinary(copyDir string) (bin pathBinary, pinned string, err error) {
	if copyDir == "" || !release.HasPin(copyDir) {
		bin, err = a.lookPathBinary()
		return bin, "", err
	}
	pinned, err = release.ReadPin(copyDir)
	if err != nil {
		return pathBinary{}, "", err
	}
	h, err := a.releaseHome()
	if err != nil {
		return pathBinary{}, pinned, err
	}
	r, err := h.Runnable(pinned)
	if err != nil {
		return pathBinary{Path: h.Binary(pinned)}, pinned, err
	}
	return pathBinary{Path: h.Binary(pinned), SHA256: r.SHA256}, pinned, nil
}

// targetName names a target binary in messages: the copy's pinned release, or the guildhall on PATH.
func targetName(pinned string) string {
	if pinned != "" {
		return "the pinned release " + pinned
	}
	return "the guildhall on PATH"
}

// reexecDecision is what a supervisor does at a reload about the guildhall binary on PATH, Binary: Move is set for
// one that differs from its own image and passed the pre-flight, Version naming its release, and Refused for one it
// refused; neither means the same binary, which it reloads on in-process as before.
type reexecDecision struct {
	Binary  pathBinary
	Version string
	Move    bool
	Refused store.ReloadRefusal
	// Pinned is the copy's pinned release when the target is the pin rather than the guildhall on PATH.
	Pinned string
}

// decideReexec resolves the guildhall binary on PATH at a reload of a supervisor whose own image hashes to own
// ("" when it could not be hashed) and, when it differs, pre-flights it as a child: `version --json` for its
// version, then `director start --check` over the supervisor's copy and runtime, which runs the full preflight
// (docs/director.md#reload).
//
// command is the session's command family, so a Steward supervisor pre-flights with `steward start --check`.
func (a *App) decideReexec(command, own, copyDir, positional string) reexecDecision {
	bin, pinned, err := a.targetBinary(copyDir)
	refuse := func(what string, err error, out []byte) reexecDecision {
		return reexecDecision{Binary: bin, Pinned: pinned, Refused: store.ReloadRefusal{SHA256: bin.SHA256, Reason: refusalReason(what, err, out)}}
	}
	if err != nil {
		if pinned != "" {
			return refuse("pinned release "+pinned, err, nil)
		}
		return refuse("guildhall on PATH", err, nil)
	}
	if own != "" && bin.SHA256 == own {
		return reexecDecision{Binary: bin, Pinned: pinned}
	}
	ctx, cancel := context.WithTimeout(context.Background(), binaryPreflightTimeout)
	defer cancel()
	out, err := a.Output(ctx, bin.Path, "version", "--json")
	if err != nil {
		return refuse("version --json", err, out)
	}
	var v versionInfo
	if err := json.NewDecoder(strings.NewReader(string(out))).Decode(&v); err != nil || v.Version == "" {
		return refuse("version --json", errors.Join(errors.New("no version reported"), err), out)
	}
	if pinned != "" && v.Version != pinned {
		return refuse("version --json", fmt.Errorf("the pinned release %s reports version %s", pinned, v.Version), nil)
	}
	args := []string{command, "start", "--check", "--copy", copyDir}
	if positional != "" {
		args = append(args, positional)
	}
	if out, err := a.Output(ctx, bin.Path, args...); err != nil {
		return refuse(command+" start --check", err, out)
	}
	return reexecDecision{Binary: bin, Version: v.Version, Move: true, Pinned: pinned}
}

// refusalReason is a refusal's one-line reason: what failed, the error and the last line of its output, bounded to
// store.MaxRefusedReasonBytes.
func refusalReason(what string, err error, out []byte) string {
	reason := what + ": " + oneLine(err.Error())
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if tail := oneLine(lines[len(lines)-1]); tail != "" {
		reason += ": " + tail
	}
	return truncateUTF8(reason, store.MaxRefusedReasonBytes)
}

// truncateUTF8 cuts s to at most n bytes of valid UTF-8, dropping invalid bytes and a rune cut in two.
func truncateUTF8(s string, n int) string { return strings.ToValidUTF8(s[:min(n, len(s))], "") }

// reexec moves the supervisor of session n, just recorded stopped for a reload, onto the guildhall binary on PATH
// when it differs from its own image own: it cleans up p and stops handling signals, neither of which survives an
// exec, and replaces its image with that binary, run with argv. replaced is set when the exec seam returned no
// error, as only a test's does, since a real exec does not return. Otherwise the supervisor stays on its own binary:
// with no refusal for the same binary, else with the refusal it printed to its pane, having restored what the exec
// would have dropped; err is a failure to restore it.
func (a *App) reexec(p startPlan, n int, own, copyDir string, signals chan<- os.Signal, argv []string) (refused store.ReloadRefusal, replaced bool, err error) {
	d := a.decideReexec(p.sessionRole().command, own, copyDir, p.positional)
	if d.Move {
		_, _ = fmt.Fprintf(a.Stdout, "session: %d moves onto guildhall %s %s (%s)\n", n, d.Version, shortSHA(d.Binary.SHA256), d.Binary.Path)
		if p.cleanup != nil {
			p.cleanup()
		}
		signal.Stop(signals)
		env := os.Environ()
		if d.Pinned != "" {
			// As the launcher would: the new image is told the release it was chosen as.
			env = launcher.ChildEnv(env, d.Pinned)
		}
		err := a.execImageEnv(d.Binary.Path, argv, env)
		if err == nil {
			return store.ReloadRefusal{}, true, nil
		}
		signal.Notify(signals, sessionSignals...)
		d.Refused = store.ReloadRefusal{SHA256: d.Binary.SHA256, Reason: refusalReason("exec", err, nil)}
		if err := a.reinstallSkills(p); err != nil {
			return d.Refused, false, fmt.Errorf("skills: %w", err)
		}
	}
	if d.Refused == (store.ReloadRefusal{}) {
		return d.Refused, false, nil
	}
	path := d.Binary.Path
	switch {
	case d.Pinned != "" && path == "":
		path = "pinned " + d.Pinned
	case path == "":
		path = "on PATH"
	}
	_, _ = fmt.Fprintf(a.Stdout, "session: %d reloads on this binary, refusing guildhall %s %s: %s\n", n, path, shortSHA(d.Refused.SHA256), d.Refused.Reason)
	return d.Refused, false, nil
}

// shortSHA is a sha256 shortened to its first 12 characters, or "(unhashed)" for none.
func shortSHA(sum string) string {
	if sum == "" {
		return "(unhashed)"
	}
	return "sha256:" + sum[:min(12, len(sum))]
}

// execImageEnv replaces this process's image with the binary at path, run with argv after its own name and the
// environment env; a real exec returns only on failure.
func (a *App) execImageEnv(path string, argv, env []string) error {
	run := a.Exec
	if run == nil {
		run = syscall.Exec
	}
	if err := run(path, append([]string{path}, argv...), env); err != nil {
		return fmt.Errorf("exec %s: %w", path, err)
	}
	return nil
}

// reinstallSkills installs the release skills and baseline again into p's own directories, which the cleanup
// before a failed exec removed.
func (a *App) reinstallSkills(p startPlan) error {
	if p.skills == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p.skills), 0o700); err != nil {
		return err
	}
	return errors.Join(a.InstallSkills(p.skills), a.InstallBaseline(p.baseline))
}
