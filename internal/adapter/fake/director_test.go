package fake

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/adaptertest"
)

func script(steps ...string) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(SessionDirective + " " + s + "\n")
	}
	return b.String()
}

func TestFakeDirectorSessionContract(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.RunSessionContract(t, func(t *testing.T) adaptertest.SessionSubject {
		return adaptertest.SessionSubject{Runtime: Adapter{Executable: exe}, Script: script}
	})
}

func TestFakeDirectorSessionCommand(t *testing.T) {
	c := t.TempDir()
	dir := filepath.Join(c, "director", "session-1")
	spec := adapter.SessionSpec{Dir: dir, Copy: c, RolePrompt: filepath.Join(dir, "role.md"),
		FirstMessage: "Start.", Model: "m", Env: []string{"A=1"}}
	cmd, err := Adapter{Executable: "/bin/guildhall"}.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", DirectorSubcommand, "--role", spec.RolePrompt, "Start."}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.Dir || !slices.Equal(cmd.Env, []string{"GUILDHALL_COPY=" + c, "A=1"}) {
		t.Errorf("command = %+v, want %q in %s", cmd, want, spec.Dir)
	}
	if cmd, err := (Adapter{}).Session(spec); err == nil || len(cmd.Argv) > 0 {
		t.Errorf("no executable: %+v, %v", cmd, err)
	}
	if got := (Adapter{}).SessionFiles("/c"); len(got) != 0 || (Adapter{}).TurnEndHook() {
		t.Errorf("SessionFiles = %v, TurnEndHook %v; want none and no hook", got, (Adapter{}).TurnEndHook())
	}
}

func TestRunScriptRefusesUnknownSteps(t *testing.T) {
	for _, bad := range []string{"rm -rf /", "exit 200", "exit x", "/quit", "git status"} {
		var stderr strings.Builder
		if code := RunScript(SessionDirective, script("guildhall director ready", bad), nil, &stderr, &stderr); code != 2 {
			t.Errorf("%q: exit %d, want 2 (%s)", bad, code, stderr.String())
		}
	}
}

// TestRunScriptCountsTurns: with TurnLogEnv naming a file, the session records one turn for its first message and
// one for each non-blank line typed to it, the /exit that ends it excepted, each with its process ID; without it, nothing.
func TestRunScriptCountsTurns(t *testing.T) {
	log := filepath.Join(t.TempDir(), "turns")
	t.Setenv(TurnLogEnv, log)
	var out strings.Builder
	if code := RunScript(SessionDirective, "", strings.NewReader("guildhall: 1 unread (note 1)\n\n/exit\nafter\n"), &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	if want := pid + " start\n" + pid + " input\n"; string(b) != want {
		t.Errorf("turn log %q, want %q", b, want)
	}

	t.Setenv(TurnLogEnv, "")
	if code := RunScript(SessionDirective, "", strings.NewReader("hello\n"), &out, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if after, _ := os.ReadFile(log); string(after) != string(b) {
		t.Errorf("a session without %s recorded turns: %q", TurnLogEnv, after)
	}
}
