package launch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/launch"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// guildhallEnv makes the test binary run the guildhall command line, so
// Process starts a real `guildhall artisan start` process.
const guildhallEnv = "GUILDHALL_LAUNCH_TEST_GUILDHALL"

// instructions are the fake Artisan directives that produce each outcome.
var instructions = map[interptest.Outcome]string{
	interptest.Succeeds: "",
	interptest.Fails:    "guildhall-fake: outcome=failed",
	interptest.Hangs:    "guildhall-fake: delay=10m",
}

func TestProcessPassesLauncherContract(t *testing.T) {
	interptest.RunLauncherContract(t, func(t *testing.T) interptest.Harness {
		p := newProcess(t)
		return interptest.Harness{Launcher: p, Adapter: interp.FakeAdapter, Prepare: func(t *testing.T, o interptest.Outcome, render func(string) interp.Spec) interp.Spec {
			return prepared(t, p, render(instructions[o]))
		}}
	})
}

// prepared records the fixture attempt app/t7/build/1 running in p's store and moves its rendered
// prompt into the attempt layout under p's state directory, as the interpreter prepares it.
func prepared(t *testing.T, p *launch.Process, spec interp.Spec) interp.Spec {
	t.Helper()
	s, err := store.Open(p.Store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	task := store.Task{Repository: "app", ID: "t7", Title: "Task", Workflow: workflow.Revision{Name: "default", Digest: fixtureDigest}, ConfigDigest: fixtureDigest}
	if _, err := s.CreateTask("task", task); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartAttempt("start", "app", "t7", "build"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}.Dir(p.StateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt")
	if err := os.Rename(filepath.Dir(spec.PromptFile), prompt); err != nil {
		t.Fatal(err)
	}
	spec.PromptFile = filepath.Join(prompt, filepath.Base(spec.PromptFile))
	spec.ResultFile = filepath.Join(dir, artisan.ResultFile)
	return spec
}

const fixtureDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// newProcess returns a Process over a new store and short state directory, running the test binary as guildhall.
func newProcess(t *testing.T) *launch.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state, err := os.MkdirTemp("", "lp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	return &launch.Process{Executable: exe, Env: []string{guildhallEnv + "=1"}, Store: filepath.Join(t.TempDir(), "director.store"), StateDir: state}
}

func fixtureSpec(t *testing.T, instructions string) interp.Spec {
	t.Helper()
	a := interptest.Build(t, "spec-v1.md")
	a.Adapter = interp.FakeAdapter
	if instructions != "" {
		a.Step.Settings["instructions"] = []byte(`"` + instructions + `"`)
	}
	return a.Render(t)
}

// argvEnv makes the test binary record its arguments as JSON in the file it names and exit 2,
// as `guildhall artisan start` exits on refused arguments.
const argvEnv = "GUILDHALL_LAUNCH_TEST_ARGV"

func recordArgv(path string) {
	b, err := json.Marshal(os.Args[1:])
	if err == nil {
		err = os.WriteFile(path, b, 0o600)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(2)
}

// TestProcessStartsArtisanStartOnTheAdapterSpec: the launcher's argv carries the engine's adapter name
// unchanged and the prompt file and working directory of spec.AdapterSpec, the mapping the adapter
// conformance suite runs runtimes on; a launcher refusing its arguments is Launch's error, naming its log.
func TestProcessStartsArtisanStartOnTheAdapterSpec(t *testing.T) {
	p := newProcess(t)
	argv := filepath.Join(t.TempDir(), "argv.json")
	p.Env = []string{argvEnv + "=" + argv}
	spec := prepared(t, p, fixtureSpec(t, ""))
	_, err := p.Launch(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), launch.LogFile) {
		t.Fatalf("Launch of a refusing launcher = %v, want an error naming %s", err, launch.LogFile)
	}
	b, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	as := spec.AdapterSpec()
	k := artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}
	want := []string{"artisan", "start", "--store", p.Store, "--state", p.StateDir,
		"--repo", k.Repository, "--task", k.TaskID, "--step", k.Step, "--attempt", "1",
		"--adapter", spec.Adapter, "--prompt", as.PromptFile, "--workdir", as.WorkDir}
	if !slices.Equal(got, want) {
		t.Errorf("argv = %q\nwant %q", got, want)
	}
}

func TestProcessRefusesASpecOutsideItsAttemptLayout(t *testing.T) {
	p := newProcess(t)
	good := prepared(t, p, fixtureSpec(t, ""))
	for name, breakSpec := range map[string]func(s *interp.Spec){
		"another attempt id":   func(s *interp.Spec) { s.Identity.AttemptID = "build-2" },
		"another task":         func(s *interp.Spec) { s.Identity.TaskID = "t8" },
		"result outside":       func(s *interp.Spec) { s.ResultFile = filepath.Join(t.TempDir(), artisan.ResultFile) },
		"result of other name": func(s *interp.Spec) { s.ResultFile = filepath.Join(filepath.Dir(s.ResultFile), "out.json") },
	} {
		spec := good
		breakSpec(&spec)
		if _, err := p.Launch(context.Background(), spec); err == nil {
			t.Errorf("%s: Launch succeeded", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(good.ResultFile), launch.LogFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused launch started a process: %v", err)
	}
}

func TestProcessDuplicateLeavesOwnerRunning(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, instructions[interptest.Hangs]))
	h, err := p.Launch(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(h) })
	duplicate := &launch.Process{Executable: p.Executable, Env: p.Env, Store: p.Store, StateDir: p.StateDir}
	if _, err := duplicate.Launch(context.Background(), spec); err == nil {
		t.Fatal("duplicate launch succeeded")
	}
	at := launch.Attempt{Store: p.Store, StateDir: p.StateDir, Key: artisan.AttemptKey{Repository: "app", TaskID: "t7", Step: "build", Attempt: 1}}
	var o store.Ownership
	withStore(t, at, func(s *store.Store) {
		var err error
		o, err = s.Ownership(at.Key.Repository, at.Key.TaskID, at.Key.Step, at.Key.Attempt)
		if err != nil {
			t.Fatal(err)
		}
	})
	if o.State == store.OwnerReleased {
		t.Fatal("duplicate launch released the original owner")
	}
	if _, err := artisan.Call(context.Background(), o.Endpoint, at.Key, artisan.Identity(o.LauncherIdentity), artisan.OpStatus); err != nil {
		t.Fatalf("original owner no longer serves status: %v", err)
	}
	if err := p.Stop(h); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(h); err != nil {
		t.Fatal(err)
	}
}

func TestProcessLaunchFailsWhenTheLauncherExitsFirst(t *testing.T) {
	p := newProcess(t)
	spec := prepared(t, p, fixtureSpec(t, ""))
	// The store records no such attempt, so `artisan start` refuses ownership and exits.
	p.Store = filepath.Join(t.TempDir(), "other.store")
	s, err := store.Open(p.Store, store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := p.Launch(context.Background(), spec); err == nil {
		t.Fatal("Launch succeeded for an attempt the store does not record")
	}
	log, err := os.ReadFile(filepath.Join(filepath.Dir(spec.ResultFile), launch.LogFile))
	if err != nil || len(log) == 0 {
		t.Errorf("launcher log = %q, %v; want the refusal", log, err)
	}
}
