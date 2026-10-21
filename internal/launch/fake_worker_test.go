package launch_test

import (
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/launch"
)

// TestStopContractFakeArtisan runs the stop ladder and precedence contract
// with the fake Artisan as the worker, through the fake adapter's command, on
// this platform's units and the real attempt files (F2).
func TestStopContractFakeArtisan(t *testing.T) {
	h := artisantest.Native(func(t *testing.T) artisan.Unit {
		u, err := artisan.NewUnit("fake-" + rand.Text()[:8])
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
	h.Worker = fakeWorker
	artisantest.RunStopContract(t, h)
}

// fakeWorker is the fake Artisan playing w: its prompt carries the P0
// directives that ask for w's tree and behaviour.
func fakeWorker(t *testing.T, inv string, w artisantest.Worker, f artisantest.StopFiles) *exec.Cmd {
	t.Helper()
	if w.SetsidGrandchild || w.BreakawayGrandchild || w.ExitAfterSpawn || w.Release != "" {
		t.Fatalf("the fake Artisan does not play %+v", w)
	}
	lines := []string{"inventory=" + inv, fmt.Sprintf("grandchildren=%d", w.Grandchildren)}
	if w.Lifetime > 0 {
		lines = append(lines, "lifetime="+w.Lifetime.String())
	}
	if w.IgnoreInterrupt {
		lines = append(lines, "ignore-interrupt=true")
	}
	if w.CancelMarker == "" {
		lines = append(lines, "ignore-cancel=true")
	}
	switch {
	case w.ResultMarker != "":
		lines = append(lines, "result=after-cancel")
	case w.Result != "":
		lines = append(lines, "result=delay", "delay="+w.ResultAfter.String())
	default:
		lines = append(lines, "result=never")
	}
	work := t.TempDir()
	prompt := filepath.Join(work, "prompt.md")
	if err := os.WriteFile(prompt, []byte("guildhall-fake: "+strings.Join(lines, "\nguildhall-fake: ")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := fake.Adapter{Executable: exe}.Command(adapter.Spec{Identity: id, WorkDir: work, PromptFile: prompt, ResultFile: f.Result})
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir, c.Env = cmd.Dir, append(os.Environ(), cmd.Env...)
	return c
}
