package fake

import (
	"os"
	"slices"
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
	spec := adapter.SessionSpec{Dir: "/c/director/session-1", Copy: "/c", RolePrompt: "/c/director/session-1/role.md",
		FirstMessage: "Start.", Model: "m", Env: []string{"A=1"}}
	cmd, err := Adapter{Executable: "/bin/guildhall"}.Session(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/guildhall", DirectorSubcommand, "--role", spec.RolePrompt, "Start."}
	if !slices.Equal(cmd.Argv, want) || cmd.Dir != spec.Dir || !slices.Equal(cmd.Env, []string{"GUILDHALL_COPY=/c", "A=1"}) {
		t.Errorf("command = %+v, want %q in %s", cmd, want, spec.Dir)
	}
	if cmd, err := (Adapter{}).Session(spec); err == nil || len(cmd.Argv) > 0 {
		t.Errorf("no executable: %+v, %v", cmd, err)
	}
	if got := (Adapter{}).SessionFiles("/c"); len(got) != 0 {
		t.Errorf("SessionFiles = %v, want none", got)
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
