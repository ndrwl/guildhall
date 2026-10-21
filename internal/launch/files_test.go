package launch_test

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/cli"
	"github.com/ndrwl/guildhall/internal/launch"
)

func TestMain(m *testing.M) {
	artisantest.MainIfWorker()
	mainIfDyingLauncher()
	if len(os.Args) > 1 && os.Args[1] == fake.Subcommand {
		os.Exit(fake.Main(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	if path := os.Getenv(argvEnv); path != "" {
		recordArgv(path)
	}
	if os.Getenv(guildhallEnv) != "" {
		os.Exit(cli.New().Run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

var id = adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}

// resultBody returns a valid version 1 result for id with status s.
func resultBody(t *testing.T, rid adapter.Identity, s adapter.Status) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "result.json")
	if err := adapter.WriteResult(p, adapter.Result{Identity: rid, Status: s, Summary: "done"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFilesReadTheAdaptersResultAndMarker(t *testing.T) {
	f := launch.Files{ResultFile: filepath.Join(t.TempDir(), "result.json"), Identity: id}
	check := func(name string, wantValid, wantMarked bool) {
		t.Helper()
		if v, err := f.ValidResult(); v != wantValid || err != nil {
			t.Errorf("%s: ValidResult() = %v, %v; want %v", name, v, err, wantValid)
		}
		if m, err := f.Marked(); m != wantMarked || err != nil {
			t.Errorf("%s: Marked() = %v, %v; want %v", name, m, err, wantMarked)
		}
	}
	check("nothing published", false, false)
	if _, err := f.Result(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Result() with no file = %v, want fs.ErrNotExist", err)
	}
	for name, body := range map[string]string{
		"malformed":      "garbage",
		"other attempt":  resultBody(t, adapter.Identity{TaskID: "t-1", AttemptID: "build-2"}, adapter.StatusSucceeded),
		"newer version":  strings.Replace(resultBody(t, id, adapter.StatusSucceeded), `"version": 1`, `"version": 2`, 1),
		"the draft name": "",
	} {
		path := f.ResultFile
		if name == "the draft name" {
			path += ".tmp"
			body = resultBody(t, id, adapter.StatusSucceeded)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		check(name, false, false)
		_ = os.Remove(path)
	}
	if err := os.WriteFile(f.ResultFile, []byte(resultBody(t, id, adapter.StatusFailed)), 0o600); err != nil {
		t.Fatal(err)
	}
	check("valid", true, false)
	if r, err := f.Result(); err != nil || r.Status != adapter.StatusFailed || r.Identity != id {
		t.Errorf("Result() = %+v, %v", r, err)
	}
	for range 2 {
		if err := f.Mark(); err != nil {
			t.Fatalf("Mark() = %v", err)
		}
	}
	check("marked with a late result", true, true)
	if ok, err := adapter.Cancelled(f.ResultFile, id); !ok || err != nil {
		t.Errorf("the adapter does not see the marker: %v, %v", ok, err)
	}
}

func TestFilesRefuseAnotherAttemptsMarker(t *testing.T) {
	result := filepath.Join(t.TempDir(), "result.json")
	if err := adapter.MarkCancelled(result, adapter.Identity{TaskID: "t-1", AttemptID: "build-2"}); err != nil {
		t.Fatal(err)
	}
	f := launch.Files{ResultFile: result, Identity: id}
	if m, err := f.Marked(); !errors.Is(err, adapter.ErrIdentityMismatch) {
		t.Errorf("Marked() = %v, %v; want ErrIdentityMismatch", m, err)
	}
	if err := f.Mark(); !errors.Is(err, adapter.ErrIdentityMismatch) {
		t.Errorf("Mark() = %v; want ErrIdentityMismatch", err)
	}
}

// TestStopContractRealFiles runs the stop ladder and precedence contract with
// the launcher deciding by the real attempt files on this platform's units.
func TestStopContractRealFiles(t *testing.T) {
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, err := artisan.NewUnit("launch-" + rand.Text()[:8])
		if err != nil {
			t.Fatal(err)
		}
		return u
	})
	h.Terminate = runtime.GOOS != "windows"
	body := resultBody(t, id, adapter.StatusSucceeded)
	h.Files = func(dir string) artisantest.StopFiles {
		f := launch.Files{ResultFile: filepath.Join(dir, artisan.ResultFile), Identity: id}
		return artisantest.StopFiles{Result: f.ResultFile, Marker: f.ResultFile + ".cancelled", Reader: f, Body: body}
	}
	artisantest.RunStopContract(t, h)
}
