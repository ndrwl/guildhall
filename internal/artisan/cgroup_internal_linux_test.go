package artisan

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCgroupHost is a host for detectCgroup: file contents, the paths that
// exist and the paths this user may write.
type fakeCgroupHost struct {
	files    map[string]string
	exist    []string
	writable []string
}

func (h fakeCgroupHost) env() cgroupEnv {
	return cgroupEnv{
		readFile: func(p string) ([]byte, error) {
			if s, ok := h.files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		exists:   func(p string) bool { return slices.Contains(h.exist, p) || slices.Contains(h.writable, p) },
		writable: func(p string) bool { return slices.Contains(h.writable, p) },
	}
}

const (
	v2Mount     = "128 119 0:25 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime shared:23 - cgroup2 cgroup2 rw,nsdelegate\n"
	v1Mount     = "30 24 0:26 / /sys/fs/cgroup ro,nosuid,nodev,noexec shared:9 - tmpfs tmpfs ro,mode=755\n31 30 0:27 / /sys/fs/cgroup/memory rw,nosuid shared:10 - cgroup cgroup rw,memory\n"
	hybridMount = v1Mount + "32 30 0:28 / /sys/fs/cgroup/unified rw,nosuid shared:11 - cgroup2 cgroup2 rw\n"
	userScope   = "/user.slice/user-1000.slice/user@1000.service/app.slice/pane.scope"
	wslRelease  = "6.18.54.1-microsoft-standard-WSL2\n"
)

// delegated is a host whose cgroup at path the user may write.
func delegated(path, release string) fakeCgroupHost {
	dir := "/sys/fs/cgroup" + strings.TrimSuffix(path, "/")
	return fakeCgroupHost{
		files:    map[string]string{"/proc/self/mountinfo": v2Mount, "/proc/self/cgroup": "0::" + path + "\n", "/proc/sys/kernel/osrelease": release},
		exist:    []string{dir + "/cgroup.kill"},
		writable: []string{dir, dir + "/cgroup.procs"},
	}
}

// TestDetectCgroup is the detection table: only a cgroup v2 mounted at
// /sys/fs/cgroup whose own cgroup this user may write, with cgroup.kill,
// selects the cgroup backend; every other host falls back with its reason.
func TestDetectCgroup(t *testing.T) {
	systemService := delegated("/system.slice/runner.service", "6.8.0-generic\n")
	systemService.writable = nil
	wslRoot := delegated("/", wslRelease)
	wslRoot.writable = nil
	noKill := delegated(userScope, "5.10.0\n")
	noKill.exist = nil
	procsLocked := delegated(userScope, "6.8.0\n")
	procsLocked.writable = procsLocked.writable[:1]
	container := delegated(userScope, "6.8.0\n")
	container.files["/proc/self/mountinfo"] = strings.Replace(v2Mount, " / /sys/fs/cgroup", " /docker/abc /sys/fs/cgroup", 1)
	container.files["/proc/self/cgroup"] = "0::/docker/abc/app\n"
	container.writable = []string{"/sys/fs/cgroup/app", "/sys/fs/cgroup/app/cgroup.procs"}
	container.exist = []string{"/sys/fs/cgroup/app/cgroup.kill"}
	outside := delegated(userScope, "6.8.0\n")
	outside.files["/proc/self/mountinfo"] = strings.Replace(v2Mount, " / /sys/fs/cgroup", " /docker/abc /sys/fs/cgroup", 1)
	v1 := fakeCgroupHost{files: map[string]string{"/proc/self/mountinfo": v1Mount, "/proc/self/cgroup": "4:memory:/user.slice\n"}}
	hybrid := fakeCgroupHost{files: map[string]string{"/proc/self/mountinfo": hybridMount, "/proc/self/cgroup": "0::/user.slice\n4:memory:/user.slice\n"}}
	none := fakeCgroupHost{files: map[string]string{"/proc/self/mountinfo": "22 1 0:21 / /proc rw - proc proc rw\n"}}

	cases := []struct {
		name   string
		host   fakeCgroupHost
		ok     bool
		dir    string
		reason string
	}{
		{"v2 delegated: systemd user manager", delegated(userScope, "6.8.0-generic\n"), true, "/sys/fs/cgroup" + userScope, "systemd user manager"},
		{"v2 delegated: WSL with systemd", delegated(userScope, wslRelease), true, "/sys/fs/cgroup" + userScope, "WSL"},
		{"v2 delegated: cgroup namespace root", container, true, "/sys/fs/cgroup/app", "delegated"},
		{"v2 not delegated: system service", systemService, false, "", "not writable"},
		{"v2 not delegated: WSL without systemd", wslRoot, false, "", "WSL"},
		{"v2 not delegated: cgroup.procs not writable", procsLocked, false, "", "cgroup.procs"},
		{"v2 without cgroup.kill", noKill, false, "", "cgroup.kill"},
		{"v2 cgroup outside the mounted subtree", outside, false, "", "outside"},
		{"v1", v1, false, "", "cgroup v1"},
		{"hybrid v1 with unified v2", hybrid, false, "", "cgroup v1"},
		{"no cgroupfs", none, false, "", "no cgroup filesystem"},
		{"mountinfo unreadable", fakeCgroupHost{}, false, "", "mountinfo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := detectCgroup(c.host.env())
			if d.ok != c.ok || d.dir != c.dir || !strings.Contains(d.reason, c.reason) {
				t.Errorf("detectCgroup = %+v; want ok %v, dir %q, reason containing %q", d, c.ok, c.dir, c.reason)
			}
		})
	}
}

// liveCgroup returns this host's delegated cgroup or skips with the reason.
func liveCgroup(t *testing.T) cgroupDetection {
	t.Helper()
	d := detectCgroup(systemCgroupEnv)
	if !d.ok {
		t.Skipf("cgroup backend not available: %s", d.reason)
	}
	return d
}

// TestCgroupKillEmpties: Kill writes cgroup.kill, which ends a descendant
// that left the process group, and Close removes the cgroup.
func TestCgroupKillEmpties(t *testing.T) {
	d := liveCgroup(t)
	u, err := newCgroupUnit("kill-"+t.Name(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	dir := u.Containment().Name
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("cgroup %s: %v", dir, err)
	}
	pidFile := filepath.Join(t.TempDir(), "escape")
	// The grandchild leaves the process group; only the cgroup holds it.
	cmd := exec.Command("/bin/sh", "-c", `setsid sleep 60 & echo $! > "$1"; wait`, "worker", pidFile)
	worker, err := u.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var gc Identity
	for end := time.Now().Add(10 * time.Second); gc.PID == 0 && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		gc = identityIn(pidFile)
	}
	m, err := u.Members()
	if err != nil || !slices.Contains(m, worker) || !slices.Contains(m, gc) {
		t.Fatalf("Members() = %v, %v; want worker %v and grandchild %v", m, err, worker, gc)
	}
	if err := u.Kill(); err != nil {
		t.Fatal(err)
	}
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if m, err = u.Members(); err == nil && len(m) == 0 {
			break
		}
	}
	if len(m) != 0 {
		t.Fatalf("Members() after Kill = %v, %v", m, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "cgroup.procs")); err != nil || len(strings.TrimSpace(string(b))) != 0 {
		t.Errorf("cgroup.procs after Kill = %q, %v", b, err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cgroup %s after Close: %v, want removed", dir, err)
	}
}

func identityIn(pidFile string) Identity {
	b, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return Identity{}
	}
	id, _ := Lookup(pid)
	return id
}

// TestCgroupNameRefused: a name that is not one new path element, or whose
// cgroup already exists, is refused rather than joined.
func TestCgroupNameRefused(t *testing.T) {
	d := liveCgroup(t)
	for _, name := range []string{"", ".", "..", "a/b"} {
		if u, err := newCgroupUnit(name, d); !errors.Is(err, ErrContainment) {
			if u != nil {
				_ = u.Close()
			}
			t.Errorf("newCgroupUnit(%q) = %v, want ErrContainment", name, err)
		}
	}
	u, err := newCgroupUnit("taken-"+t.Name(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	if again, err := newCgroupUnit("taken-"+t.Name(), d); !errors.Is(err, ErrContainment) {
		if again != nil {
			_ = again.Close()
		}
		t.Errorf("second newCgroupUnit of one name = %v, want ErrContainment", err)
	}
}
