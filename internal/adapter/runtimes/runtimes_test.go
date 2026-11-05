package runtimes_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/adapter/claude"
	"github.com/ndrwl/guildhall/internal/adapter/codex"
	"github.com/ndrwl/guildhall/internal/adapter/fake"
	"github.com/ndrwl/guildhall/internal/adapter/runtimes"
)

func TestLookupConstructsEachRuntime(t *testing.T) {
	if got, want := runtimes.Names(), []string{adapter.ClaudeName, adapter.CodexName, adapter.FakeName}; !slices.Equal(got, want) {
		t.Fatalf("Names() = %q, want %q", got, want)
	}
	if got, want := runtimes.SessionNames(), []string{adapter.ClaudeName, adapter.FakeName}; !slices.Equal(got, want) {
		t.Fatalf("SessionNames() = %q, want %q", got, want)
	}
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "result.json")}
	for name, subcommand := range map[string]string{adapter.FakeName: fake.Subcommand, adapter.ClaudeName: claude.Subcommand, adapter.CodexName: codex.Subcommand} {
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", name, err)
		}
		cmd, err := r.Command(spec)
		if err != nil || len(cmd.Argv) < 2 || cmd.Argv[0] != exe || cmd.Argv[1] != subcommand || cmd.Dir != dir {
			t.Errorf("%s command = %+v, %v; want %s %s in %s", name, cmd, err, exe, subcommand, dir)
		}
		if _, err := r.Command(adapter.Spec{}); err == nil {
			t.Errorf("%s built a command for an invalid spec", name)
		}
	}
}

// TestEachRuntimeDeclaresSteerable: claude and fake can be steered; codex, which runs read-only and is never told
// about steering, cannot.
func TestEachRuntimeDeclaresSteerable(t *testing.T) {
	want := map[string]bool{adapter.ClaudeName: true, adapter.FakeName: true, adapter.CodexName: false}
	for _, name := range runtimes.Names() {
		r, err := runtimes.Lookup(name, "/bin/guildhall")
		if err != nil {
			t.Fatal(err)
		}
		w, ok := want[name]
		if !ok {
			t.Errorf("runtime %q has no expected Steerable; add it here", name)
			continue
		}
		if got := r.Steerable(); got != w {
			t.Errorf("%s Steerable() = %v, want %v", name, got, w)
		}
	}
}

func TestLookupRefusesAnUnknownName(t *testing.T) {
	for _, name := range []string{"", "gemini", "mock", "Fake", "Codex"} {
		r, err := runtimes.Lookup(name, "/bin/guildhall")
		if r != nil || !errors.Is(err, runtimes.ErrUnknown) {
			t.Errorf("Lookup(%q) = %v, %v; want ErrUnknown", name, r, err)
			continue
		}
		if !strings.Contains(err.Error(), "claude, codex, fake") || name != "" && !strings.Contains(err.Error(), name) {
			t.Errorf("Lookup(%q) error %q does not name it and list claude, codex, fake", name, err)
		}
	}
}

func TestLookupSessionConstructsEachRuntime(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.SessionSpec{Dir: dir, Copy: dir, RolePrompt: filepath.Join(dir, "role.md"), FirstMessage: "Start."}
	for name, program := range map[string]string{adapter.FakeName: "", adapter.ClaudeName: claude.Program} {
		r, err := runtimes.LookupSession(name, exe)
		if err != nil {
			t.Fatalf("LookupSession(%q): %v", name, err)
		}
		if r.Program() != program {
			t.Errorf("%s Program() = %q, want %q", name, r.Program(), program)
		}
		if cmd, err := r.Session(spec); err != nil || cmd.Dir != dir || cmd.Argv[len(cmd.Argv)-1] != "Start." {
			t.Errorf("%s session = %+v, %v", name, cmd, err)
		}
	}
	// Codex runs attempts only: a Director session on it is refused, naming the session runtimes.
	if r, err := runtimes.LookupSession(adapter.CodexName, exe); r != nil || !errors.Is(err, runtimes.ErrNoSession) || errors.Is(err, runtimes.ErrUnknown) ||
		!strings.Contains(err.Error(), `"codex" runs attempts only`) || !strings.Contains(err.Error(), "claude, fake") {
		t.Errorf("LookupSession(codex) = %v, %v; want ErrNoSession listing claude, fake", r, err)
	}
	if r, err := runtimes.LookupSession("gemini", exe); r != nil || !errors.Is(err, runtimes.ErrUnknown) || !strings.Contains(err.Error(), "claude, fake") {
		t.Errorf("LookupSession(gemini) = %v, %v", r, err)
	}
}

// TestEachRuntimeChecksSettings: every runtime honours the zero Settings, the runtime's defaults, and refuses in
// Command whatever its CheckSettings refuses; the Claude runtime refuses an effort Claude Code does not take.
func TestEachRuntimeChecksSettings(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "guildhall")
	dir := t.TempDir()
	spec := adapter.Spec{Identity: adapter.Identity{TaskID: "t-1", AttemptID: "build-1"}, WorkDir: dir,
		PromptFile: filepath.Join(dir, "prompt.md"), ResultFile: filepath.Join(dir, "result.json")}
	for _, name := range runtimes.Names() {
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.CheckSettings(adapter.Settings{}); err != nil {
			t.Errorf("%s refused the defaults: %v", name, err)
		}
		bad := adapter.Settings{Effort: "extreme"}
		checked := r.CheckSettings(bad)
		spec.Settings = bad
		_, built := r.Command(spec)
		if (checked == nil) != (built == nil) || checked != nil && !errors.Is(built, adapter.ErrUnsupportedSetting) {
			t.Errorf("%s: CheckSettings = %v but Command = %v; want the same refusal", name, checked, built)
		}
	}
	r, _ := runtimes.Lookup(adapter.ClaudeName, exe)
	if err := r.CheckSettings(adapter.Settings{Effort: "extreme"}); !errors.Is(err, adapter.ErrUnsupportedSetting) || !strings.Contains(err.Error(), "extreme") {
		t.Errorf("claude CheckSettings = %v; want ErrUnsupportedSetting naming the effort", err)
	}
}

// TestEachRuntimeDeclaresItsCapabilities: every runtime declares whether it grants tools and applies a skill policy,
// with the values its adapter.Declaration gives packages that cannot import runtimes, and a sign-in entry that is
// complete, or, for the fake alone, which is the guildhall executable, the zero entry; and every session runtime
// takes SessionOptions.
func TestEachRuntimeDeclaresItsCapabilities(t *testing.T) {
	type want struct{ grants, skills, readOnly, signin bool }
	wants := map[string]want{
		adapter.ClaudeName: {grants: true, skills: true, signin: true},
		adapter.CodexName:  {readOnly: true, signin: true},
		adapter.FakeName:   {},
	}
	exe := filepath.Join(t.TempDir(), "guildhall")
	for _, name := range runtimes.Names() {
		w, ok := wants[name]
		if !ok {
			t.Errorf("runtime %q: no expected capabilities; add it to this test", name)
			continue
		}
		r, err := runtimes.Lookup(name, exe)
		if err != nil {
			t.Fatal(err)
		}
		if r.GrantsTools() != w.grants || r.SkillPolicy() != w.skills {
			t.Errorf("%s: GrantsTools() = %v, SkillPolicy() = %v; want %v, %v", name, r.GrantsTools(), r.SkillPolicy(), w.grants, w.skills)
		}
		d, ok := adapter.Declared(name)
		if !ok {
			t.Errorf("%s: package adapter declares nothing for it", name)
		}
		if d != (adapter.Declaration{GrantsTools: r.GrantsTools(), SkillPolicy: r.SkillPolicy(), ReadOnly: w.readOnly}) ||
			adapter.GrantsTools(name) != r.GrantsTools() || adapter.SkillPolicy(name) != r.SkillPolicy() || adapter.ReadOnly(name) != w.readOnly {
			t.Errorf("%s: adapter.Declared = %+v, disagreeing with its methods or a read-only %v", name, d, w.readOnly)
		}
		s := r.Signin()
		switch {
		case !w.signin && !s.Zero():
			t.Errorf("%s: Signin() = %+v, want the zero entry", name, s)
		case w.signin && (s.Title == "" || s.Program == "" || len(s.Status) == 0 || len(s.Verify) == 0 || len(s.Act) == 0):
			t.Errorf("%s: Signin() = %+v, want a title, program, status, verify and act lines", name, s)
		}
	}
	for _, name := range runtimes.SessionNames() {
		s, err := runtimes.LookupSession(name, exe)
		if err != nil {
			t.Fatal(err)
		}
		got := s.SessionOptions(nil, &adapter.Links{URL: "http://host:1", Repos: []string{"r"}})
		if got.GrantsTools() != s.GrantsTools() || got.Program() != s.Program() {
			t.Errorf("%s: SessionOptions changed the runtime", name)
		}
	}
	for _, name := range []string{"", "gemini"} {
		if _, ok := adapter.Declared(name); ok || adapter.GrantsTools(name) || adapter.SkillPolicy(name) || adapter.ReadOnly(name) {
			t.Errorf("adapter declares %q, which is no runtime", name)
		}
	}
}

// TestClaudeSessionOptions: the Claude session runtime renders the links SessionOptions gives it, a nil argument
// keeps what an earlier call gave, and the runtime stays a PersistentSession.
func TestClaudeSessionOptions(t *testing.T) {
	s, err := runtimes.LookupSession(adapter.ClaudeName, "/bin/guildhall")
	if err != nil {
		t.Fatal(err)
	}
	copyDir := t.TempDir()
	if _, ok := s.SessionFiles(copyDir)[claude.LinksFile]; ok {
		t.Fatal("a session with no links rendered the links file")
	}
	s = s.SessionOptions(nil, &adapter.Links{URL: "http://host:1"})
	s = s.SessionOptions(nil, nil)
	if got, want := string(s.SessionFiles(copyDir)[claude.LinksFile]), `{"url":"http://host:1","repos":[]}`+"\n"; got != want {
		t.Errorf("links file = %q, want %q", got, want)
	}
	if _, ok := s.(runtimes.PersistentSession); !ok {
		t.Error("the Claude session runtime is no longer a PersistentSession")
	}
}
