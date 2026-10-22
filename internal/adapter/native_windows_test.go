package adapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

// TestWindowsNativeWorkerContract runs the nativeWorker contract against a
// real handle on a live helper process.
func TestWindowsNativeWorkerContract(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testNativeWorker(t, func(t *testing.T) nativeUnderTest {
		dir := t.TempDir()
		release, createdFile := filepath.Join(dir, "release"), filepath.Join(dir, "created")
		cmd := exec.Command(exe, "-test.run=^TestNativeWorkerChildHelper$", "--", "native-child", release, createdFile)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		exited := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(exited)
		}()
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			<-exited
		})
		var created uint64
		deadline := time.Now().Add(testenv.Ceiling(30 * time.Second))
		for {
			b, err := os.ReadFile(createdFile)
			if err == nil && strings.HasSuffix(string(b), "\n") {
				created, err = strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("helper did not report its creation time")
			}
			time.Sleep(10 * time.Millisecond)
		}
		w, err := openNativeWorker(uint32(cmd.Process.Pid), created, exe)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.close() })
		return nativeUnderTest{
			w: w,
			exited: func() bool {
				select {
				case <-exited:
					return true
				default:
					return false
				}
			},
			finish: func() {
				if err := os.WriteFile(release, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		}
	})
}

// TestNativeWorkerChildHelper is not a test: it is the process
// TestWindowsNativeWorkerContract holds a handle on. It reports its creation
// time, then runs until released.
func TestNativeWorkerChildHelper(t *testing.T) {
	at := slices.Index(os.Args, "--")
	if at < 0 || len(os.Args) != at+4 || os.Args[at+1] != "native-child" {
		t.Skip("process for TestWindowsNativeWorkerContract")
	}
	created, err := nativeCreationTime()
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(os.Args[at+3], []byte(strconv.FormatUint(created, 10)+"\n"), 0o600); err != nil {
		os.Exit(2)
	}
	for {
		if _, err := os.Stat(os.Args[at+2]); err == nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
