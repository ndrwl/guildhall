package steward

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a stub scp: when stubRootEnv is set, a copy of it
// named scp copies files between local paths and a fake remote tree, so tests
// cross a real process boundary on every OS without ssh, a network or a server.
const (
	stubRootEnv    = "GUILDHALL_STUB_SCP_ROOT"
	stubFailEnv    = "GUILDHALL_STUB_SCP_FAIL"
	stubCorruptEnv = "GUILDHALL_STUB_SCP_CORRUPT"
	stubHangEnv    = "GUILDHALL_STUB_SCP_HANG"
)

func TestMain(m *testing.M) {
	if ready := os.Getenv("GUILDHALL_STUB_SSH_CHILD"); ready != "" {
		if err := os.WriteFile(ready+".pid", []byte(fmt.Sprint(os.Getpid())), 0o600); err != nil {
			os.Exit(1)
		}
		// Publish readiness only after closing the PID file. Renaming the file
		// can race with readers on Windows while the rename handle is open.
		if err := os.Mkdir(ready, 0o700); err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Minute) // timing:allow deliberately hung scp subprocess; parent owns timeout and reap.
		os.Exit(0)
	}
	if private := os.Getenv(ownerPrivateEnv); private != "" {
		os.Exit(lockOwner(private, os.Getenv(ownerPlanEnv)))
	}
	if root := os.Getenv(stubRootEnv); root != "" {
		os.Exit(stubSCP(root, os.Args[1:]))
	}
	os.Exit(m.Run())
}

var remoteOperand = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]+):(.+)$`)

func stubSCP(root string, args []string) int {
	log, err := os.OpenFile(filepath.Join(root, "scp.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = fmt.Fprintln(log, strings.Join(args, " "))
		if cerr := log.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stub scp:", err)
		return 1
	}
	want := []string{"-q", "-B", "-o", "BatchMode=yes", "--"}
	if len(args) != len(want)+2 || strings.Join(args[:len(want)], " ") != strings.Join(want, " ") {
		fmt.Fprintf(os.Stderr, "stub scp: unexpected arguments %q\n", args)
		return 1
	}
	src, dst := args[len(want)], args[len(want)+1]
	joined := src + " " + dst
	if f := os.Getenv(stubFailEnv); f != "" && strings.Contains(joined, f) {
		fmt.Fprintln(os.Stderr, "stub scp: connection closed")
		return 1
	}
	if h := os.Getenv(stubHangEnv); h != "" && strings.Contains(joined, h) {
		if executable := os.Getenv("GUILDHALL_STUB_SCP_CHILD"); executable != "" {
			child := exec.Command(executable)
			child.Env = append(os.Environ(), "GUILDHALL_STUB_SSH_CHILD="+filepath.Join(root, "child-ready"))
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
		}
		time.Sleep(time.Hour) // timing:allow deliberately hung scp subprocess until the caller kills it.
	}
	data, err := os.ReadFile(stubPath(root, src))
	if err != nil {
		fmt.Fprintln(os.Stderr, "stub scp:", err)
		return 1
	}
	if c := os.Getenv(stubCorruptEnv); c != "" && strings.Contains(joined, c) {
		data = append(data, "# corrupted in transit\n"...)
	}
	if err := os.WriteFile(stubPath(root, dst), data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "stub scp:", err)
		return 1
	}
	return 0
}

// stubPath maps alias:path operands into the fake remote tree and leaves local paths alone.
func stubPath(root, operand string) string {
	m := remoteOperand.FindStringSubmatch(operand)
	if m == nil {
		return operand
	}
	return filepath.Join(root, "remote", m[1], filepath.FromSlash(strings.ReplaceAll(m[2], ":", "")))
}

// stub is a fake fleet reached through the stub scp.
type stub struct {
	t    *testing.T
	root string
	scp  string
}

func newStub(t *testing.T) *stub {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	name := "scp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	scp := filepath.Join(t.TempDir(), name)
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scp, data, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stubRootEnv, root)
	t.Setenv(stubFailEnv, "")
	t.Setenv(stubCorruptEnv, "")
	t.Setenv(stubHangEnv, "")
	return &stub{t: t, root: root, scp: scp}
}

func (s *stub) transport() Transport {
	return Transport{
		Path:    s.scp,
		Timeout: time.Minute,
	}
}

// put writes a file on a fake remote machine.
func (s *stub) put(alias, path, content string) {
	s.t.Helper()
	p := stubPath(s.root, alias+":"+path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		s.t.Fatal(err)
	}
}

// get reads a file from a fake remote machine, or "" if it is absent.
func (s *stub) get(alias, path string) string {
	s.t.Helper()
	data, err := os.ReadFile(stubPath(s.root, alias+":"+path))
	if err != nil {
		return ""
	}
	return string(data)
}

// calls returns the scp argument lines run so far and forgets them.
func (s *stub) calls() []string {
	s.t.Helper()
	p := filepath.Join(s.root, "scp.log")
	data, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		s.t.Fatal(err)
	}
	_ = os.Remove(p)
	var lines []string
	for line := range bytes.Lines(data) {
		lines = append(lines, strings.TrimSpace(string(line)))
	}
	return lines
}

// StubFleet exposes the stub scp fleet to tests outside the package, such as the shared Copier contract.
type StubFleet struct{ s *stub }

// NewStubFleet returns a fake fleet with no files, reached through a stub scp.
func NewStubFleet(t *testing.T) StubFleet { return StubFleet{newStub(t)} }

// Transport returns the scp Transport that runs the stub.
func (f StubFleet) Transport() Transport { return f.s.transport() }

// Write sets a file on a machine.
func (f StubFleet) Write(file Remote, data []byte) {
	f.s.t.Helper()
	f.s.put(file.Alias, file.Path, string(data))
}

// Read returns a file on a machine and whether it exists.
func (f StubFleet) Read(file Remote) ([]byte, bool) {
	data, err := os.ReadFile(stubPath(f.s.root, file.String()))
	return data, err == nil
}

// Refuse makes the stub fail every copy naming the machine with alias.
func (f StubFleet) Refuse(alias string) { f.s.t.Setenv(stubFailEnv, alias+":") }

// Hang makes the stub wait, without copying, on every copy naming the machine with alias.
func (f StubFleet) Hang(alias string) { f.s.t.Setenv(stubHangEnv, alias+":") }
