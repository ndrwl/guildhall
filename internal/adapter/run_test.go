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
	"github.com/ndrwl/guildhall/internal/testenv"
)

// helperEnv makes the test binary act as a child process for process tests.
const helperEnv = "GUILDHALL_ADAPTER_TEST_HELPER"

func TestMain(m *testing.M) {
	if dir := os.Getenv(tabsStubEnv); dir != "" {
		os.Exit(stubHerdr(dir, os.Args[1:]))
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
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
	ctx, cancel := context.WithTimeout(context.Background(), testenv.Ceiling(30*time.Second))
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
	case <-time.After(testenv.Ceiling(30 * time.Second)):
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

func TestTabLauncherTypesCommandForThePaneShell(t *testing.T) {
	f := newFakeTabs("lab", "bash")
	f.busyReads = 2
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
	want := "run " + run.Tab.PaneID + ` '/bin/guild hall' 'fake-artisan' '--prompt' 'it'\''s'`
	if sent := f.sentLines(); len(sent) != 1 || sent[0] != want {
		t.Errorf("sent = %q, want %q", sent, want)
	}
	if err := run.Stop(context.Background()); err != nil || len(f.sentLines()) != 2 || f.sentLines()[1] != "keys "+run.Tab.PaneID+" ctrl+c" {
		t.Errorf("Stop = %v, sent = %q", err, f.sentLines())
	}
}

// TestTabLauncherOpensThenTypes opens a tab, as the Director's wrapper does before it knows the argv that names
// the tab, and types into it only when asked: the typed line is argv quoted for the shell Open reported.
func TestTabLauncherOpensThenTypes(t *testing.T) {
	f := newFakeTabs("lab", "zsh")
	f.busyReads = 2
	l := TabLauncher{Host: f, WorkspaceID: "w1", ShellReady: time.Minute, Poll: time.Millisecond}
	tab, shell, err := l.Open(context.Background(), "director ada", "/copy", nil)
	if err != nil {
		t.Fatal(err)
	}
	if shell != "zsh" || len(f.created) != 1 || f.created[0].Label != "director ada" || f.created[0].Cwd != "/copy" {
		t.Errorf("shell %q, created %+v", shell, f.created)
	}
	if sent := f.sentLines(); len(sent) != 0 {
		t.Errorf("Open typed %q", sent)
	}
	argv := []string{"/bin/guildhall", "director", "start", "--herdr-tab", tab.TabID}
	if err := l.Type(context.Background(), tab, shell, argv); err != nil {
		t.Fatal(err)
	}
	line, _ := herdr.ShellCommand("zsh", argv)
	if sent := f.sentLines(); len(sent) != 1 || sent[0] != "run "+tab.PaneID+" "+line {
		t.Errorf("sent = %q, want the argv quoted for zsh", sent)
	}
	if err := l.Type(context.Background(), tab, "powershell", []string{"x", ""}); err == nil || len(f.sentLines()) != 1 {
		t.Errorf("Type of an argv PowerShell cannot pass = %v, sent %q", err, f.sentLines())
	}
}

func TestTabLauncherRefusesBusyOrUnknownShell(t *testing.T) {
	busy := newFakeTabs("lab", "bash")
	busy.busyReads = 1 << 30
	l := TabLauncher{Host: busy, WorkspaceID: "w1", ShellReady: 20 * time.Millisecond, Poll: time.Millisecond}
	if _, err := l.Start(context.Background(), "a", Command{Argv: []string{"x"}}); err == nil {
		t.Error("Start typed into a pane whose shell never became idle")
	}
	if sent := busy.sentLines(); len(sent) != 0 {
		t.Errorf("busy pane received %q", sent)
	}
	fish := newFakeTabs("lab", "fish")
	l.Host = fish
	if _, err := l.Start(context.Background(), "a", Command{Argv: []string{"x"}}); err == nil {
		t.Error("Start accepted an unsupported shell")
	}
	if sent := fish.sentLines(); len(sent) != 0 {
		t.Errorf("unsupported shell received %q", sent)
	}
}
