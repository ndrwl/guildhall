// Package launcher is the PATH launcher, the one guildhall on PATH once a copy adopts releases
// (docs/releases.md#the-path-launcher). It selects the copy a command names, as the CLI selects it, reads that
// copy's release.pin and runs the pinned release with the same arguments and standard streams, setting
// GUILDHALL_RELEASE for it. It loads no configuration and never falls back to another release, except for the
// recovery dispatch of `guildhall release ...`, which runs on the newest installed, verified release.
//
// Task-aware dispatch, which runs an existing task on the release it recorded, lands with task release records
// (d127 slice 3); until then every command of a copy runs on its pin, as a task created before its copy recorded
// releases does. See docs/contracts/launcher.md.
package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/release"
)

// Options is what the launcher selects from: the command's arguments, its environment and working directory, and
// the platform. The home is found from the environment alone (release.Locate), never from the launcher's own path,
// as the launcher is not an installed release.
type Options struct {
	Args   []string
	Getenv func(string) string
	Getwd  func() (string, error)
	GOOS   string
}

// Choice is the release the launcher runs a command on.
type Choice struct {
	Home release.Home
	// Copy is the selected copy directory, "" when the command selected none.
	Copy    string
	Version string
	Binary  string
	// Recovery says why recovery dispatch ran the command on the newest installed release instead of a pin; ""
	// when the copy's pin chose it.
	Recovery string
}

// SelectCopy is the copy a command with args selects, as the CLI selects one (docs/running.md#copies): --copy
// <dir> when given, else, when no --store, --config or --state is given, copy discovery from getwd. A command
// given --store without --copy does no discovery; the launcher takes that store's own directory as the copy when it
// holds a copy marker, the store's owning copy, and otherwise selects none. Two --copy flags naming different
// directories are refused. Arguments after "--" are not read.
func SelectCopy(args []string, getwd func() (string, error)) (dir string, ok bool, err error) {
	flags := map[string][]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch name {
		case "copy", "store", "config", "state":
		default:
			continue
		}
		if !hasValue {
			if i+1 >= len(args) {
				return "", false, fmt.Errorf("flag -%s needs a value", name)
			}
			i++
			value = args[i]
		}
		flags[name] = append(flags[name], value)
	}
	if copies := flags["copy"]; len(copies) > 0 {
		for _, c := range copies[1:] {
			if !copydir.SamePath(c, copies[0]) {
				return "", false, fmt.Errorf("conflicting copy selection: --copy %s and --copy %s", copies[0], c)
			}
		}
		abs, err := filepath.Abs(copies[0])
		if err != nil {
			return "", false, err
		}
		return abs, true, nil
	}
	if len(flags["store"]) > 0 || len(flags["config"]) > 0 || len(flags["state"]) > 0 {
		stores := flags["store"]
		for _, s := range stores[min(1, len(stores)):] {
			if !copydir.SamePath(s, stores[0]) {
				return "", false, fmt.Errorf("conflicting store selection: --store %s and --store %s", stores[0], s)
			}
		}
		if len(stores) == 0 {
			return "", false, nil
		}
		owner, err := filepath.Abs(filepath.Dir(stores[0]))
		if err != nil || !copydir.IsCopy(owner) {
			return "", false, nil
		}
		return owner, true, nil
	}
	found, ok, err := copydir.Discover(getwd)
	if err != nil || !ok {
		return "", false, err
	}
	return found, true, nil
}

// recoverable reports whether args is a command recovery dispatch may run with no usable pin: `guildhall release
// ...`, and, with no copy selected at all, help and version, which read and change nothing.
func recoverable(args []string, copySelected bool) bool {
	if len(args) == 0 {
		return !copySelected
	}
	switch args[0] {
	case "release":
		return true
	case "help", "-h", "--help", "version":
		return !copySelected
	}
	return false
}

// Choose selects the release o's command runs on (docs/releases.md#the-path-launcher):
//
//  1. the copy the command selects (SelectCopy);
//  2. that copy's release.pin, run when its release is installed and intact;
//  3. a pin whose release is missing or damaged is refused, naming the version, the path checked and the fix,
//     and so is a copy with no pin, naming guildhall release use, or, managed, config reload --copy;
//  4. recovery dispatch, for `guildhall release ...` only, runs on the newest installed, verified release
//     instead of refusing, and refuses when there is none; it never runs an unverified .incoming binary.
func Choose(o Options) (Choice, error) {
	goos := o.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	home, err := release.Locate(o.Getenv, "", "", goos)
	if err != nil {
		return Choice{}, err
	}
	dir, ok, err := SelectCopy(o.Args, o.Getwd)
	if err != nil {
		return Choice{}, err
	}
	c := Choice{Home: home, Copy: dir}
	recovery := func(why string) (Choice, error) {
		if !recoverable(o.Args, ok) {
			return Choice{}, errors.New(why)
		}
		newest, found, err := home.Newest()
		if err != nil {
			return Choice{}, fmt.Errorf("%s; recovery dispatch cannot list %s: %w", why, home.ReleasesDir(), err)
		}
		if !found {
			return Choice{}, fmt.Errorf("%s; recovery dispatch found no installed, verified release under %s, and never runs an unverified %s binary: run a release binary by its path instead", why, home.ReleasesDir(), home.IncomingDir())
		}
		c.Version, c.Binary, c.Recovery = newest.Version, home.Binary(newest.Version), why
		return c, nil
	}
	if !ok {
		return recovery("no Guildhall copy selected: pass --copy <dir> or cd into a copy; the launcher runs the release a copy pins")
	}
	managed := false
	if _, err := os.Lstat(filepath.Join(dir, "managed.toml")); err == nil {
		managed = true
	}
	pin, err := release.ReadPin(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		fix := "set one with guildhall release use <version>, or adopt releases with guildhall release adopt"
		if managed {
			fix = "a managed copy takes it from its rollout with guildhall config reload --copy " + dir
		}
		return recovery(fmt.Sprintf("copy %s has no release pin (%s); %s", dir, release.PinPath(dir), fix))
	case err != nil:
		return recovery(fmt.Sprintf("copy %s: %v", dir, err))
	}
	if _, err := home.Runnable(pin); err != nil {
		fix := fmt.Sprintf("install it with guildhall release install %s --from <file> --sha256 <hex>", pin)
		if managed {
			fix += ", or, managed, by rollout"
		}
		return recovery(fmt.Sprintf("copy %s: its pinned %v; the launcher never falls back to another release: %s", dir, err, fix))
	}
	c.Version, c.Binary = pin, home.Binary(pin)
	return c, nil
}

// ChildEnv is env with every GUILDHALL_RELEASE and launcher probe entry removed and GUILDHALL_RELEASE set to
// version: an inherited value is never a selection input, and the child always sees the release it is.
func ChildEnv(env []string, version string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, release.Env) || strings.EqualFold(k, release.LauncherProbeEnv) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, release.Env+"="+version)
}

// Main is the launcher's whole run over args, with the process's environment and standard streams: under
// LauncherProbeEnv it prints its report; otherwise it chooses the release and runs it, returning its exit code.
// On Unix a successful run replaces the launcher's image and never returns.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Getenv(release.LauncherProbeEnv) == "1" {
		b, _ := json.Marshal(release.LauncherReport{Schema: release.LauncherSchema, Version: release.LauncherVersion})
		if _, err := fmt.Fprintf(stdout, "%s\n", b); err != nil {
			return 1
		}
		return 0
	}
	c, err := Choose(Options{Args: args, Getenv: os.Getenv, Getwd: os.Getwd, GOOS: runtime.GOOS})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: launcher: %v\n", err)
		return 1
	}
	if c.Recovery != "" {
		_, _ = fmt.Fprintf(stderr, "guildhall: launcher: recovery dispatch: %s; running on %s, the newest installed, verified release\n", c.Recovery, c.Version)
	}
	code, err := run(c.Binary, args, ChildEnv(os.Environ(), c.Version), stdin, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "guildhall: launcher: %s: %v\n", c.Binary, err)
		return 1
	}
	return code
}
