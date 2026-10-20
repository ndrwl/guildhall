package artisan_test

import (
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"golang.org/x/sys/windows"
)

func TestIdentityExit259Worker(t *testing.T) {
	if os.Getenv("ARTISAN_EXIT259_WORKER") != "1" {
		return
	}
	var release [1]byte
	if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
		os.Exit(1)
	}
	os.Exit(259)
}

func TestIdentityExited259(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestIdentityExit259Worker$")
	cmd.Env = append(os.Environ(), "ARTISAN_EXIT259_WORKER=1")
	release, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(h) }()
	id, err := artisan.Lookup(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if lv, err := artisan.Probe(id); lv != artisan.Alive || err != nil {
		t.Fatalf("Probe before release = %v, %v; want alive", lv, err)
	}
	if _, err := release.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if state, err := windows.WaitForSingleObject(h, 10000); state != windows.WAIT_OBJECT_0 || err != nil {
		t.Fatalf("wait for exit = %v, %v", state, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != 259 {
		t.Fatalf("exit code = %d, %v; want 259", code, err)
	}
	if lv, err := artisan.Probe(id); lv != artisan.Gone || err != nil {
		t.Fatalf("Probe after exit 259 with retained handle = %v, %v; want gone", lv, err)
	}
}
