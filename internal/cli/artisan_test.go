package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude/claudetest"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/artisan/artisantest"
	"github.com/ndrwl/guildhall/internal/herdr"
	"github.com/ndrwl/guildhall/internal/store"
)

// roleEnv makes the test binary act as another program in child processes:
// "guildhall" runs the CLI, "herdr" answers like a Herdr client.
const roleEnv = "GUILDHALL_CLI_TEST_ROLE"

func TestMain(m *testing.M) {
	store.SkipSyncForTests()
	artisantest.MainIfWorker()
	claudetest.RunIfStub()
	runStewardSCPStub()
	switch os.Getenv(roleEnv) {
	case "guildhall":
		// As cmd/guildhall assembles it, with an empty release skills root and the release baseline: a launcher's advance installs them.
		app := New()
		app.InstallSkills = func(string) error { return nil }
		app.InstallBaseline = baseline.Install
		os.Exit(app.Run(os.Args[1:]))
	case "herdr":
		os.Exit(fakeHerdr(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeHerdr replays recorded Herdr responses from internal/herdr/testdata. It
// records `pane run` lines in FAKE_HERDR_LOG and, when FAKE_HERDR_RESULT is set,
// writes a valid result there as an Artisan in the pane would.
func fakeHerdr(args []string) int {
	if len(args) < 4 || args[len(args)-2] != "--session" || args[len(args)-1] != "lab" {
		fmt.Fprintf(os.Stderr, "fake herdr: call without --session lab: %q\n", args)
		return 2
	}
	fixtures := filepath.Join("..", "herdr", "testdata")
	reply := func(name string) int {
		b, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, _ = os.Stdout.Write(bytes.ReplaceAll(b, []byte("guildhall-lab"), []byte("lab")))
		return 0
	}
	switch args[0] + " " + args[1] {
	case "status --json":
		return reply("status.json")
	case "tab create":
		return reply("tab_create.json")
	case "pane process-info":
		return reply("process_info_idle.json")
	case "pane run":
		f, err := os.OpenFile(os.Getenv("FAKE_HERDR_LOG"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return 2
		}
		_, err = fmt.Fprintln(f, args[3])
		if errors.Join(err, f.Close()) != nil {
			return 2
		}
		if path := os.Getenv("FAKE_HERDR_RESULT"); path != "" {
			id := adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"}
			if err := adapter.WriteResult(path, adapter.Result{Identity: id, Status: adapter.StatusSucceeded, Summary: "from the pane"}); err != nil {
				return 2
			}
		}
		return 0
	case "pane send-keys":
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake herdr: unexpected call %q\n", args)
	return 2
}

func testExecutable(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// artisanFiles writes a prompt and returns its path, a result path and a work dir.
func artisanFiles(t *testing.T, prompt string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "prompt.md")
	if err := os.WriteFile(p, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, filepath.Join(dir, "result.json"), dir
}

func newArtisanApp(t *testing.T) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	app, stdout, stderr := newTestApp(nil, nil)
	exe := testExecutable(t)
	app.Executable = func() (string, error) { return exe, nil }
	return app, stdout, stderr
}

func TestFakeArtisanCommandWritesResult(t *testing.T) {
	prompt, result, _ := artisanFiles(t, "Do it.\n")
	app, stdout, stderr := newArtisanApp(t)
	code := app.Run([]string{"fake-artisan", "--task", "task-1", "--attempt", "attempt-1", "--prompt", prompt, "--result", result})
	if code != 0 {
		t.Fatalf("exit %d; stderr %s", code, stderr)
	}
	r, err := adapter.ReadResult(result, adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"})
	if err != nil || r.Status != adapter.StatusSucceeded {
		t.Errorf("result = %+v, %v", r, err)
	}
	if !strings.Contains(stdout.String(), "wrote succeeded result") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestFakeArtisanCommandUsageErrors(t *testing.T) {
	app, _, stderr := newArtisanApp(t)
	if code := app.Run([]string{"fake-artisan", "--task", "t"}); code != 2 {
		t.Errorf("exit %d, want 2; stderr %s", code, stderr)
	}
}

func TestArtisanStartRefusesExistingResultAndBadFlags(t *testing.T) {
	prompt, result, dir := artisanFiles(t, "x")
	if err := os.WriteFile(result, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, _, stderr := newArtisanApp(t)
	base := []string{"artisan", "start", "--adapter", "fake", "--task", "task-1", "--attempt", "attempt-1", "--prompt", prompt, "--result", result, "--workdir", dir}
	inTab := append(append([]string{}, base...), "--herdr-session", "lab", "--herdr-workspace", "w1")
	if code := app.Run(inTab); code != 1 || !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("existing result: exit %d, stderr %q", code, stderr)
	}
	for _, bad := range [][]string{
		{"artisan", "start", "--adapter", "claude", "--herdr-session", "lab", "--herdr-workspace", "w1"},
		{"artisan", "launch"},
		{"artisan"},
		append(append([]string{}, base...), "--herdr-session", "lab"),
		append(append([]string{}, base...), "--herdr-session", "default", "--herdr-workspace", "w1"),
	} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(bad); code == 0 {
			t.Errorf("%q accepted; stderr %q", bad, stderr)
		}
	}
	// The stall interval is an owned launcher's, never negative.
	for bad, want := range map[string]string{"-1s": "must not be negative", "1m": "do not apply in a Herdr tab"} {
		app, _, stderr := newArtisanApp(t)
		if code := app.Run(append(append([]string{}, inTab...), "--stall-interval", bad)); code != 2 || !strings.Contains(stderr.String(), want) {
			t.Errorf("--stall-interval %s in a tab: exit %d, stderr %q; want 2 naming %q", bad, code, stderr, want)
		}
	}
}

type artisanTestWriter func([]byte) (int, error)

func (w artisanTestWriter) Write(p []byte) (int, error) { return w(p) }

func TestArtisanStartInHerdrTabTypesCommandAndWaitsForFile(t *testing.T) {
	t.Setenv(roleEnv, "herdr")
	log := filepath.Join(t.TempDir(), "herdr.log")
	t.Setenv("FAKE_HERDR_LOG", log)
	prompt, result, dir := artisanFiles(t, "Do it.\n")
	app, stdout, stderr := newArtisanApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	app.Stderr = artisanTestWriter(func(p []byte) (int, error) {
		n, err := stderr.Write(p)
		if bytes.Contains(p, []byte("artisan started in Herdr")) {
			cancel()
		}
		return n, err
	})
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "task-1", AttemptID: "attempt-1"},
		PromptFile: prompt, ResultFile: result, WorkDir: dir}
	cmd, err := (fake.Adapter{Executable: testExecutable(t)}).Command(spec)
	if err != nil {
		t.Fatal(err)
	}
	code := app.runInTab(ctx, spec, cmd, herdr.Options{Session: "lab", Command: []string{testExecutable(t)}}, "w1", "artisan task-1/attempt-1", false)
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("launch did not reach synchronized cancellation: %v; stderr %s", ctx.Err(), stderr)
	}
	if code != 3 || !strings.Contains(stderr.String(), "no result yet") || !strings.Contains(stderr.String(), "w1:t2") {
		t.Errorf("exit %d; stdout %q; stderr %q", code, stdout, stderr)
	}
	ran, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ran), "'fake-artisan' '--task' 'task-1' '--attempt' 'attempt-1'") {
		t.Errorf("pane run line = %q", ran)
	}
}

func TestArtisanStartInHerdrTabRecordsResultFromFile(t *testing.T) {
	t.Setenv(roleEnv, "herdr")
	t.Setenv("FAKE_HERDR_LOG", filepath.Join(t.TempDir(), "herdr.log"))
	prompt, result, dir := artisanFiles(t, "Do it.\n")
	t.Setenv("FAKE_HERDR_RESULT", result)
	app, stdout, stderr := newArtisanApp(t)
	code := app.Run([]string{"artisan", "start", "--adapter", "fake", "--task", "task-1", "--attempt", "attempt-1",
		"--prompt", prompt, "--result", result, "--workdir", dir,
		"--herdr-session", "lab", "--herdr-workspace", "w1", "--herdr-exec", testExecutable(t), "--timeout", "30s"})
	if code != 0 || !strings.Contains(stdout.String(), `"summary":"from the pane"`) {
		t.Errorf("exit %d; stdout %q; stderr %q", code, stdout, stderr)
	}
}
