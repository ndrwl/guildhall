// Package launcher is the PATH launcher, the one guildhall on PATH once a copy adopts releases
// (docs/releases.md#the-path-launcher). It selects the copy a command names, as the CLI selects it, reads that
// copy's release.pin and runs the pinned release with the same arguments and standard streams, setting
// GUILDHALL_RELEASE for it. It loads no configuration and never falls back to another release, except for the
// recovery dispatch of `guildhall release ...`, which runs on the newest installed, verified release.
//
// A task command, one that advances, launches or resumes an existing task (TaskCommand), runs instead on the
// release its task recorded at task new, read from the task's store, whatever the copy pins; a task that recorded
// none, created before its copy recorded releases, runs on the pin. See docs/contracts/launcher.md.
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
	"time"

	"github.com/ndrwl/guildhall/internal/copydir"
	"github.com/ndrwl/guildhall/internal/release"
	"github.com/ndrwl/guildhall/internal/store"
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
	// when the copy's pin or a task's recorded release chose it.
	Recovery string
	// Task is the recorded release of the task a task command names when that chose the release; nil when the
	// command is not a task command or its task recorded none, which runs on the pin.
	Task *store.TaskRelease
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

// Task is the task a task command names: its repository and id.
type Task struct {
	Repository string
	ID         string
}

// positionalTaskCommands are the task commands that name their task by their one positional argument, with
// --repo, each with the flags it takes that have no value; every other flag of theirs takes one.
var positionalTaskCommands = map[string]map[string]bool{
	"task start":     {"ignore-deps": true},
	"task cancel":    {},
	"task reconcile": {},
	"quality judge":  {},
}

// flagTaskCommands are the task commands that name their task by --repo and --task.
var flagTaskCommands = map[string]bool{
	"step done": true, "step finish": true, "step retry": true, "step send-back": true, "step park": true, "step unpark": true,
	"artisan start": true, "artisan resume": true, "quality judged": true, "task design": true,
}

// TaskCommand reports whether args is a task command, one that advances, launches or resumes an existing task,
// and the task it names (docs/releases.md#tasks-keep-their-release). A task command without both its repository
// and its task, or naming either twice with different values, is refused: it is never treated as new work. A task
// command asking for help is not one, so its usage prints from the copy's pin.
func TaskCommand(args []string) (Task, bool, error) {
	if len(args) < 2 {
		return Task{}, false, nil
	}
	cmd := args[0] + " " + args[1]
	bools, positional := positionalTaskCommands[cmd]
	if !positional && !flagTaskCommands[cmd] {
		return Task{}, false, nil
	}
	values := map[string][]string{}
	var ids []string
	for i := 2; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			ids = append(ids, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			ids = append(ids, a)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		// A command naming its task by flags has no positional arguments to tell from flag values, so only --repo
		// and --task are read with theirs.
		takesValue := !bools[name] && (positional || name == "repo" || name == "task")
		switch {
		case name == "h" || name == "help":
			return Task{}, false, nil
		case hasValue || !takesValue:
		case i+1 < len(args):
			i++
			value = args[i]
		default:
			return Task{}, true, fmt.Errorf("%s: flag -%s needs a value", cmd, name)
		}
		if name == "repo" || name == "task" {
			values[name] = append(values[name], value)
		}
	}
	if positional {
		values["task"] = append(values["task"], ids...)
	}
	var t Task
	for _, f := range []struct {
		name string
		to   *string
	}{{"repo", &t.Repository}, {"task", &t.ID}} {
		vs := values[f.name]
		if len(vs) == 0 || vs[0] == "" {
			what := "--task"
			if positional {
				what = "the task id"
			}
			return Task{}, true, fmt.Errorf("%s names no task: it needs --repo and %s, and a task command is never run as new work", cmd, what)
		}
		for _, v := range vs[1:] {
			if v != vs[0] {
				return Task{}, true, fmt.Errorf("%s names conflicting tasks: %s %s and %s", cmd, f.name, vs[0], v)
			}
		}
		*f.to = vs[0]
	}
	return t, true, nil
}

// storeWait bounds the launcher's wait for a store's lock while it reads a task's release.
const storeWait = 10 * time.Second

// taskStore is the store a task command reads: --store when given, else the selected copy's director.store; ""
// when neither names one.
func taskStore(args []string, copyDir string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "store" {
			continue
		}
		if !hasValue && i+1 < len(args) {
			value = args[i+1]
		}
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	if copyDir == "" {
		return ""
	}
	return filepath.Join(copyDir, DirectorStore)
}

// DirectorStore is a Director copy's store file, beside its copy marker.
const DirectorStore = "director.store"

// taskRelease is the release task t recorded in the store at path, with ok false when it recorded none or the store
// does not exist, so the command runs as a task created before its copy recorded releases does, on the pin.
func taskRelease(path string, t Task) (store.TaskRelease, bool, error) {
	if path == "" {
		return store.TaskRelease{}, false, nil
	}
	r, err := store.ReadTaskRelease(path, t.Repository, t.ID, storeWait)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return store.TaskRelease{}, false, nil
	case err != nil:
		return store.TaskRelease{}, false, err
	}
	return r, true, nil
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
//  2. for a task command (TaskCommand), the release its task recorded in its store (--store, else the copy's
//     director.store), run when it is installed, intact and has the recorded sha256, and otherwise refused, naming
//     the version: a task never falls back, nor moves to the pin; a task that recorded none goes on to the pin;
//  3. that copy's release.pin, run when its release is installed and intact;
//  4. a pin whose release is missing or damaged is refused, naming the version, the path checked and the fix,
//     and so is a copy with no pin, naming guildhall release use, or, managed, config reload --copy;
//  5. recovery dispatch, for `guildhall release ...` only, runs on the newest installed, verified release
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
	task, isTask, err := TaskCommand(o.Args)
	if err != nil {
		return Choice{}, err
	}
	if isTask {
		path := taskStore(o.Args, dir)
		rec, found, err := taskRelease(path, task)
		if err != nil {
			return Choice{}, fmt.Errorf("task %s/%s: cannot read its recorded release: %w", task.Repository, task.ID, err)
		}
		if found {
			c.Task = &rec
			r, err := home.Runnable(rec.Version)
			if err == nil && r.SHA256 != rec.SHA256 {
				err = fmt.Errorf("release %s at %s has sha256 %s, not the %s the task recorded", rec.Version, home.ReleaseDir(rec.Version), r.SHA256, rec.SHA256)
			}
			if err != nil {
				return Choice{}, fmt.Errorf("task %s/%s records release %s (sha256 %s), and a task is never moved to another release: %v; install that release with guildhall release install %s --from <file> --sha256 %s", task.Repository, task.ID, rec.Version, rec.SHA256, err, rec.Version, rec.SHA256)
			}
			c.Version, c.Binary = rec.Version, home.Binary(rec.Version)
			return c, nil
		}
	}
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
