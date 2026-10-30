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

// reexecDecision is what a supervisor does at a reload about the guildhall binary on PATH, Binary: Move is set for
// one that differs from its own image and passed the pre-flight, Version naming its release, and Refused for one it
// refused; neither means the same binary, which it reloads on in-process as before.
type reexecDecision struct {
	Binary  pathBinary
	Version string
	Move    bool
	Refused store.ReloadRefusal
}

// decideReexec resolves the guildhall binary on PATH at a reload of a supervisor whose own image hashes to own
// ("" when it could not be hashed) and, when it differs, pre-flights it as a child: `version --json` for its
// version, then `director start --check` over the supervisor's copy and runtime, which runs the full preflight
// (docs/director.md#reload).
func (a *App) decideReexec(own, copyDir, positional string) reexecDecision {
	bin, err := a.lookPathBinary()
	refuse := func(what string, err error, out []byte) reexecDecision {
		return reexecDecision{Binary: bin, Refused: store.ReloadRefusal{SHA256: bin.SHA256, Reason: refusalReason(what, err, out)}}
	}
	if err != nil {
		return refuse("guildhall on PATH", err, nil)
	}
	if own != "" && bin.SHA256 == own {
		return reexecDecision{Binary: bin}
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
	args := []string{"director", "start", "--check", "--copy", copyDir}
	if positional != "" {
		args = append(args, positional)
	}
	if out, err := a.Output(ctx, bin.Path, args...); err != nil {
		return refuse("director start --check", err, out)
	}
	return reexecDecision{Binary: bin, Version: v.Version, Move: true}
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
	d := a.decideReexec(own, copyDir, p.positional)
	if d.Move {
		_, _ = fmt.Fprintf(a.Stdout, "session: %d moves onto guildhall %s %s (%s)\n", n, d.Version, shortSHA(d.Binary.SHA256), d.Binary.Path)
		if p.cleanup != nil {
			p.cleanup()
		}
		signal.Stop(signals)
		err := a.execImage(d.Binary.Path, argv)
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
	if path == "" {
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

// execImage replaces this process's image with the binary at path, run with argv after its own name and this
// process's environment; a real exec returns only on failure.
func (a *App) execImage(path string, argv []string) error {
	run := a.Exec
	if run == nil {
		run = syscall.Exec
	}
	if err := run(path, append([]string{path}, argv...), os.Environ()); err != nil {
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
