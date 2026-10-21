package artisan

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// cgroupMount is where the cgroup backend requires a cgroup v2 hierarchy.
const cgroupMount = "/sys/fs/cgroup"

// cgroupEnv is what detection reads of the host.
type cgroupEnv struct {
	readFile func(path string) ([]byte, error)
	exists   func(path string) bool
	writable func(path string) bool
}

var systemCgroupEnv = cgroupEnv{
	readFile: os.ReadFile,
	exists:   func(p string) bool { _, err := os.Stat(p); return err == nil },
	writable: func(p string) bool { return unix.Access(p, unix.W_OK) == nil },
}

// cgroupDetection is the result of detectCgroup: when ok, dir is the
// launcher's own cgroup directory and procPath the same cgroup as
// /proc/<pid>/cgroup names it. reason says why either way.
type cgroupDetection struct {
	ok       bool
	dir      string
	procPath string
	reason   string
}

// detectCgroup decides whether the launcher can contain workers in child
// cgroups of its own: a cgroup v2 hierarchy is mounted at /sys/fs/cgroup, the
// launcher's cgroup is inside the mounted subtree, this user may create a
// directory in it and write its cgroup.procs (a delegated subtree, such as a
// systemd user manager's), and cgroup.kill exists (Linux 5.14). No controller
// is enabled, so cgroup.subtree_control is not needed.
func detectCgroup(env cgroupEnv) cgroupDetection {
	host := ""
	if rel, err := env.readFile("/proc/sys/kernel/osrelease"); err == nil && bytes.Contains(bytes.ToLower(rel), []byte("microsoft")) {
		host = " (WSL)"
	}
	no := func(format string, a ...any) cgroupDetection {
		return cgroupDetection{reason: fmt.Sprintf(format, a...) + host}
	}
	mi, err := env.readFile("/proc/self/mountinfo")
	if err != nil {
		return no("cannot read /proc/self/mountinfo: %v", err)
	}
	fstype, root, found := mountAt(mi, cgroupMount)
	switch {
	case !found:
		return no("no cgroup filesystem mounted at %s", cgroupMount)
	case fstype != "cgroup2":
		return no("cgroup v1 or hybrid hierarchy at %s (%s)", cgroupMount, fstype)
	}
	self, err := env.readFile("/proc/self/cgroup")
	if err != nil {
		return no("cannot read /proc/self/cgroup: %v", err)
	}
	path, found := "", false
	for line := range strings.SplitSeq(string(self), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			path, found = p, true
		}
	}
	if !found {
		return no("no cgroup v2 entry in /proc/self/cgroup")
	}
	rel := path
	if root != "/" {
		if path != root && !strings.HasPrefix(path, root+"/") {
			return no("launcher cgroup %s is outside the mounted subtree %s", path, root)
		}
		rel = strings.TrimPrefix(path, root)
	}
	dir := filepath.Join(cgroupMount, rel)
	switch {
	case !env.writable(dir):
		return no("cgroup v2 at %s, but the launcher cgroup %s is not writable by this user: not a delegated subtree", cgroupMount, path)
	case !env.writable(filepath.Join(dir, "cgroup.procs")):
		return no("cgroup v2 at %s, but %s/cgroup.procs is not writable by this user", cgroupMount, path)
	case !env.exists(filepath.Join(dir, "cgroup.kill")):
		return no("cgroup v2 at %s has no cgroup.kill (needs Linux 5.14)", cgroupMount)
	}
	kind := "delegated"
	if strings.Contains(path, "/user@") && strings.Contains(path, ".service") {
		kind = "delegated by the systemd user manager"
	}
	return cgroupDetection{ok: true, dir: dir, procPath: path, reason: fmt.Sprintf("cgroup v2 %s at %s", kind, path) + host}
}

// mountAt returns the filesystem type and mount root of the topmost mount at
// point in mountinfo.
func mountAt(mountinfo []byte, point string) (fstype, root string, found bool) {
	sc := bufio.NewScanner(bytes.NewReader(mountinfo))
	for sc.Scan() {
		pre, post, ok := strings.Cut(sc.Text(), " - ")
		f, g := strings.Fields(pre), strings.Fields(post)
		if !ok || len(f) < 5 || len(g) < 1 || f[4] != point {
			continue
		}
		fstype, root, found = g[0], f[3], true
	}
	return fstype, root, found
}

// cgroup is the child cgroup a groupUnit also contains its worker in.
type cgroup struct {
	dir  string // directory under cgroupMount
	proc string // the path /proc/<pid>/cgroup shows for a member
}

// newCgroupUnit creates guildhall-<name> in the detected cgroup; a name that
// is not one path element, or whose cgroup exists, is refused.
func newCgroupUnit(name string, d cgroupDetection) (Unit, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, fmt.Errorf("%w: invalid unit name %q", ErrContainment, name)
	}
	base := "guildhall-" + name
	c := &cgroup{dir: filepath.Join(d.dir, base), proc: strings.TrimSuffix(d.procPath, "/") + "/" + base}
	if err := os.Mkdir(c.dir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: create cgroup: %v", ErrContainment, err)
	}
	u := newGroupUnit(Containment{Kind: KindCgroup, Name: c.dir, Reason: d.reason})
	u.cgroup = c
	return u, nil
}

// place creates the worker inside the cgroup (CLONE_INTO_CGROUP); release
// closes the directory once the worker exists.
func (c *cgroup) place(attr *syscall.SysProcAttr) (release func(), err error) {
	fd, err := unix.Open(c.dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open cgroup %s: %v", ErrContainment, c.dir, err)
	}
	attr.UseCgroupFD, attr.CgroupFD = true, fd
	return func() { _ = unix.Close(fd) }, nil
}

// holds reports whether pid is in the cgroup.
func (c *cgroup) holds(pid int) (bool, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			return p == c.proc, nil
		}
	}
	return false, nil
}

func (c *cgroup) pids() ([]int, error) {
	b, err := os.ReadFile(filepath.Join(c.dir, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	var out []int
	for f := range strings.FieldsSeq(string(b)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("cgroup.procs of %s: %w", c.dir, err)
		}
		out = append(out, pid)
	}
	return out, nil
}

// containedCgroup lists the live processes in the cgroup directory dir from
// outside its unit; a directory that no longer exists holds nothing.
func containedCgroup(dir string) ([]Identity, error) {
	pids, err := (&cgroup{dir: dir}).pids()
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Identity
	for _, pid := range pids {
		id, err := Lookup(pid)
		switch {
		case errors.Is(err, errNoProcess):
		case err != nil:
			return nil, err
		default:
			out = append(out, id)
		}
	}
	return out, nil
}

// members lists the live processes in the cgroup.
func (c *cgroup) members() ([]Identity, error) {
	pids, err := c.pids()
	if err != nil {
		return nil, err
	}
	var out []Identity
	for _, pid := range pids {
		id, err := Lookup(pid)
		switch {
		case errors.Is(err, errNoProcess):
			continue
		case err != nil:
			return nil, err
		}
		if in, err := c.holds(pid); err != nil || !in {
			if err != nil {
				return nil, err
			}
			continue
		}
		current, err := Lookup(pid)
		switch {
		case errors.Is(err, errNoProcess):
		case err != nil:
			return nil, err
		case current == id:
			out = append(out, id)
		}
	}
	return out, nil
}

// signal delivers sig to every process in the cgroup: SIGKILL through
// cgroup.kill, anything else through a pidfd taken before membership is
// re-checked, so a reused PID is never signalled.
func (c *cgroup) signal(sig unix.Signal) error {
	if sig == unix.SIGKILL {
		// A removed cgroup was empty: rmdir refuses one with processes.
		err := os.WriteFile(filepath.Join(c.dir, "cgroup.kill"), []byte("1"), 0)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	pids, err := c.pids()
	if err != nil {
		return err
	}
	var errs []error
	for _, pid := range pids {
		errs = append(errs, c.signalOne(pid, sig))
	}
	return errors.Join(errs...)
}

func (c *cgroup) signalOne(pid int, sig unix.Signal) error {
	fd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if in, err := c.holds(pid); err != nil || !in {
		return err
	}
	if err := unix.PidfdSendSignal(fd, sig, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	return nil
}

// remove deletes the emptied cgroup, waiting briefly for exiting processes
// to leave it.
func (c *cgroup) remove() error {
	for end := time.Now().Add(deadlineRemove); ; time.Sleep(20 * time.Millisecond) {
		err := unix.Rmdir(c.dir)
		switch {
		case err == nil || errors.Is(err, unix.ENOENT):
			return nil
		case !errors.Is(err, unix.EBUSY) || time.Now().After(end):
			return fmt.Errorf("remove cgroup %s: %w", c.dir, err)
		}
	}
}

// deadlineRemove bounds how long remove waits for the cgroup to empty.
const deadlineRemove = 5 * time.Second
