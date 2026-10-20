package adapter

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/herdr"
)

// helperEnv makes the test binary act as a child process for process tests.
const helperEnv = "GUILDHALL_ADAPTER_TEST_HELPER"

func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "write-result":
		err := WriteResult(os.Getenv("RESULT"), Result{Identity: testID, Status: StatusSucceeded, Summary: "child " + os.Getenv("MARK")})
		if err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	case "sleep":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "exit":
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func helperCommand(t *testing.T, mode string, env ...string) Command {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Command{Argv: []string{exe, "-test.run=^$"}, Dir: t.TempDir(), Env: append([]string{helperEnv + "=" + mode}, env...)}
}

func TestStartProcessCompletesFromResultFile(t *testing.T) {
	result := filepath.Join(t.TempDir(), "r.json")
	p, err := StartProcess(helperCommand(t, "write-result", "RESULT="+result, "MARK=env-passed"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := WaitResult(ctx, result, testID, WaitOptions{Poll: 10 * time.Millisecond, Exited: p.Exited()})
	if err != nil || r.Summary != "child env-passed" {
		t.Fatalf("WaitResult = %+v, %v", r, err)
	}
}

func TestStartProcessExitWithoutResult(t *testing.T) {
	p, err := StartProcess(helperCommand(t, "exit"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = WaitResult(ctx, filepath.Join(t.TempDir(), "r.json"), testID, WaitOptions{Poll: 10 * time.Millisecond, Exited: p.Exited()})
	if !errors.Is(err, ErrExitedWithoutResult) {
		t.Errorf("WaitResult = %v, want ErrExitedWithoutResult", err)
	}
}

func TestProcessStop(t *testing.T) {
	p, err := StartProcess(helperCommand(t, "sleep"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Exited():
	case <-time.After(30 * time.Second):
		t.Fatal("process did not exit after Stop")
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop = %v", err)
	}
}

func TestStartProcessRefusesEmptyCommand(t *testing.T) {
	if _, err := StartProcess(Command{}, nil, nil); err == nil {
		t.Error("StartProcess accepted an empty command")
	}
}

// fakeTabs is a tabHost that records calls and reports a shell that becomes
// idle after a number of busy reads.
type fakeTabs struct {
	created    []herdr.TabSpec
	ran        []string
	interrupts int
	busyReads  int
	shell      string
	runErr     error
}

func (f *fakeTabs) TabCreate(_ context.Context, spec herdr.TabSpec) (herdr.OwnedTab, error) {
	f.created = append(f.created, spec)
	return herdr.OwnedTab{}, nil
}

func (f *fakeTabs) PaneProcessInfo(context.Context, string) (herdr.ProcessInfo, error) {
	if f.busyReads > 0 {
		f.busyReads--
		return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 11, Name: "rc-script"}}}, nil
	}
	return herdr.ProcessInfo{ShellPID: 10, Foreground: []herdr.Process{{PID: 10, Name: f.shell}}}, nil
}

func (f *fakeTabs) RunInOwnedTab(_ context.Context, _ herdr.OwnedTab, line string) error {
	f.ran = append(f.ran, line)
	return f.runErr
}

func (f *fakeTabs) InterruptOwnedTab(context.Context, herdr.OwnedTab) error {
	f.interrupts++
	return nil
}

func TestTabLauncherTypesCommandForThePaneShell(t *testing.T) {
	f := &fakeTabs{shell: "bash", busyReads: 2}
	l := TabLauncher{Host: f, WorkspaceID: "w1", ShellReady: time.Second, Poll: time.Millisecond}
	cmd := Command{Argv: []string{"/bin/guild hall", "fake-artisan", "--prompt", "it's"}, Dir: "/work", Env: []string{"A=1"}}
	run, err := l.Start(context.Background(), "artisan task-1", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0].WorkspaceID != "w1" || f.created[0].Label != "artisan task-1" ||
		f.created[0].Cwd != "/work" || !slices.Equal(f.created[0].Env, []string{"A=1"}) {
		t.Errorf("created = %+v", f.created)
	}
	want := `'/bin/guild hall' 'fake-artisan' '--prompt' 'it'\''s'`
	if len(f.ran) != 1 || f.ran[0] != want {
		t.Errorf("ran = %q, want %q", f.ran, want)
	}
	if err := run.Stop(context.Background()); err != nil || f.interrupts != 1 {
		t.Errorf("Stop = %v, interrupts = %d", err, f.interrupts)
	}
}

func TestTabLauncherRefusesBusyOrUnknownShell(t *testing.T) {
	busy := &fakeTabs{shell: "bash", busyReads: 1 << 30}
	l := TabLauncher{Host: busy, WorkspaceID: "w1", ShellReady: 20 * time.Millisecond, Poll: time.Millisecond}
	if _, err := l.Start(context.Background(), "a", Command{Argv: []string{"x"}}); err == nil {
		t.Error("Start typed into a pane whose shell never became idle")
	}
	if len(busy.ran) != 0 {
		t.Errorf("busy pane received %q", busy.ran)
	}
	fish := &fakeTabs{shell: "fish"}
	l.Host = fish
	if _, err := l.Start(context.Background(), "a", Command{Argv: []string{"x"}}); err == nil {
		t.Error("Start accepted an unsupported shell")
	}
	if len(fish.ran) != 0 {
		t.Errorf("unsupported shell received %q", fish.ran)
	}
}
