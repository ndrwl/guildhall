package render

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/skills"
)

func sessionInput(t *testing.T) SessionInput {
	t.Helper()
	return SessionInput{
		Dir: filepath.Join(t.TempDir(), "session-3"), Number: 3,
		Role:         Source{Name: "roles/director.md", Path: filepath.Join("testdata", "baseline", "roles", "director.md"), Root: "copy"},
		SkillsDir:    ".claude/skills",
		RuntimeFiles: map[string][]byte{".claude/settings.json": []byte("{}\n")},
		InFlight: []string{
			"retry: attempt 1 of build failed (app/t-3): guildhall step retry --repo app --task t-3 --step build --attempt 1",
			"messages: 2 unread Director messages: guildhall messages list director",
		},
		Notes: []string{
			"### promised",
			"- n2 app/t-3 (s2, 1d): tell the Guildmaster when t-3 merges",
			"### guildmaster",
			"- n4 (s2, 3h): prefers one-line replies",
			"### plan",
			"- n1 (s1, 2d): rebase t-5 and t-6 one at a time after t-3 merges",
			"- n5 app/t-2 (s2, 5m): drop t-2 if t-3 covers it [task done, check it]",
		},
		NotesBytes: 160,
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func TestSessionGolden(t *testing.T) {
	in := sessionInput(t)
	r, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	startup, err := os.ReadFile(r.StartupPath)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "session-startup.md", startup)
	manifest, err := os.ReadFile(filepath.Join(in.Dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "session.manifest.json", manifest)
	var onDisk SessionManifest
	readJSON(t, filepath.Join(in.Dir, ManifestFile), &onDisk)
	if mustJSON(t, onDisk) != mustJSON(t, r.Manifest) {
		t.Errorf("returned manifest differs from %s:\n%s\n%s", ManifestFile, mustJSON(t, r.Manifest), mustJSON(t, onDisk))
	}
	for _, s := range r.Manifest.Skills {
		if _, err := os.Stat(filepath.Join(in.Dir, filepath.FromSlash(s.Path), "SKILL.md")); err != nil {
			t.Errorf("skill %s not copied to %s: %v", s.Name, s.Path, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(in.Dir, ".claude", "settings.json")); err != nil || string(got) != "{}\n" {
		t.Errorf("runtime file = %q, %v", got, err)
	}
	if r.RolePath != filepath.Join(in.Dir, RoleFile) || r.SkillsPath != filepath.Join(in.Dir, ".claude", "skills") {
		t.Errorf("paths = %+v", r)
	}
}

func TestSessionStartupWithNoNotesAndNothingInFlight(t *testing.T) {
	in := sessionInput(t)
	in.InFlight, in.Notes, in.NotesBytes = nil, nil, 0
	r, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.StartupPath)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "session-startup-no-notes.md", b)
	for _, want := range []string{"You are starting up.", "## In flight\n\nNone.\n", "guildhall director ready"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("startup.md lacks %q:\n%s", want, b)
		}
	}
	for _, not := range []string{"## Notes", "andoff", "Parked", "## Running"} {
		if strings.Contains(string(b), not) {
			t.Errorf("startup.md with no notes or parked attempts has %q:\n%s", not, b)
		}
	}
}

// TestSessionStartupWarnsOverEightKBOfNotes: the compact warning opens the notes section only when the notes' text
// totals more than NotesWarnBytes.
func TestSessionStartupWarnsOverEightKBOfNotes(t *testing.T) {
	for _, c := range []struct {
		name   string
		bytes  int
		golden string
	}{
		{"exactly 8 KB", NotesWarnBytes, ""},
		{"over 8 KB", NotesWarnBytes + 1, "session-startup-notes-over.md"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := sessionInput(t)
			in.InFlight, in.NotesBytes = nil, c.bytes
			r, err := Session(in, roots())
			if err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(r.StartupPath)
			if err != nil {
				t.Fatal(err)
			}
			warn := "## Notes\n\nNotes total 9 KB, over 8 KB: compact them (merge, shorten or remove).\n\n### promised\n"
			if got := strings.Contains(string(b), warn); got != (c.golden != "") {
				t.Errorf("warning shown %v, want %v:\n%s", got, c.golden != "", b)
			}
			if c.golden != "" {
				golden(t, c.golden, b)
			} else if !strings.Contains(string(b), "## Notes\n\n### promised\n") || strings.Contains(string(b), "compact") {
				t.Errorf("startup.md at exactly 8 KB:\n%s", b)
			}
		})
	}
}

func TestSessionRefusesAMultiLineNote(t *testing.T) {
	in := sessionInput(t)
	in.Notes = []string{"- n1 (s1, 0m): a\nb"}
	if _, err := Session(in, roots()); err == nil || !strings.Contains(err.Error(), "must be one line") {
		t.Fatalf("multi-line note: %v", err)
	}
}

// TestSessionStartupListsParkedAttempts: parked attempts follow the in-flight list in their own section, which says
// they are held on purpose, one line each.
func TestSessionStartupListsParkedAttempts(t *testing.T) {
	in := sessionInput(t)
	in.Parked = []string{
		"parked: attempt 3 of review of app/t-4 failed; reason: Held until app/t-11 merges.; retry with `guildhall step retry --repo app --task t-4 --step review --attempt 3`",
		"parked: attempt 1 of build of lib/t-2 stopped; reason: Waiting on the Guildmaster.; retry with `guildhall step retry --repo lib --task t-2 --step build --attempt 1`",
	}
	r, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.StartupPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "- messages: 2 unread Director messages: guildhall messages list director\n" +
		"\n## Parked\n\n" +
		"These attempts are held on purpose and are not work: nothing here needs you until its hold ends.\n\n" +
		"- " + in.Parked[0] + "\n" +
		"- " + in.Parked[1] + "\n"
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("startup.md does not end with the parked section %q:\n%s", want, b)
	}
}

// TestSessionStartupListsHeldItems: a park held until tasks merge keeps its parked line with its until suffix, and
// deferred decisions and follow-ups follow in their own held section, one line each.
func TestSessionStartupListsHeldItems(t *testing.T) {
	in := sessionInput(t)
	in.Parked = []string{
		"parked: attempt 1 of build of app/t-4 failed; reason: Rebase first.; retry with `guildhall step retry --repo app --task t-4 --step build --attempt 1` until app/d45",
	}
	in.Held = []string{
		"held: decision accept-plan of app/t-5 until app/d45, app/d51 (waiting for app/d51): After the store change.; lift with `guildhall decide undefer --repo app --task t-5 --decision accept-plan`",
		"held: follow-up f2 until app/d45 (waiting for app/d45): Tell the Guildmaster.; lift with `guildhall director follow-up done f2`",
	}
	r, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.StartupPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "\n## Parked\n\n" +
		"These attempts are held on purpose and are not work: nothing here needs you until its hold ends.\n\n" +
		"- " + in.Parked[0] + "\n" +
		"\n## Held\n\n" +
		"These decisions and follow-ups wait for other tasks to merge and are not work yet: each comes back in needs_me, with a wake, once its tasks merge.\n\n" +
		"- " + in.Held[0] + "\n" +
		"- " + in.Held[1] + "\n"
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("startup.md does not end with the parked and held sections %q:\n%s", want, b)
	}
	in.Held = []string{"one\ntwo"}
	if _, err := Session(in, roots()); err == nil || !strings.Contains(err.Error(), "held item") {
		t.Errorf("a multi-line held item = %v, want it refused", err)
	}
}

// TestSessionStartupListsRunningAttempts: running attempts have their own section between the in-flight list and the
// parked attempts, which asks the Director to confirm them before it is ready, one line each.
func TestSessionStartupListsRunningAttempts(t *testing.T) {
	in := sessionInput(t)
	in.Running = []string{
		"running: attempt 1 of build of app/t-4; state running; heartbeat 20s ago; last output 3m ago; endpoint answers and launcher identity is live; check with `guildhall artisan status --repo app --task t-4 --step build --attempt 1`",
		"running: attempt 1 of plan of lib/t-2; state absent; no heartbeat; no ownership record; check with `guildhall artisan status --repo lib --task t-2 --step plan --attempt 1`",
	}
	in.Parked = []string{"parked: attempt 3 of review of app/t-4 failed; reason: Held.; retry with `guildhall step retry --repo app --task t-4 --step review --attempt 3`"}
	r, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.StartupPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "- messages: 2 unread Director messages: guildhall messages list director\n" +
		"\n## Running\n\n" +
		"These attempts are in flight: confirm each is running before you run `guildhall director ready`. One that is not healthy is also an in-flight item.\n\n" +
		"- " + in.Running[0] + "\n" +
		"- " + in.Running[1] + "\n" +
		"\n## Parked\n\n"
	if !strings.Contains(string(b), want) {
		t.Errorf("startup.md lacks the running section between In flight and Parked %q:\n%s", want, b)
	}
}

// TestSessionSkillsMatchTheRelease renders the embedded release skills and compares each tree digest with the
// release's recorded one (skills/testdata/digests/<skill>.sha256), so a session's skills are the release's byte for byte.
func TestSessionSkillsMatchTheRelease(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	if err := skills.Install(release); err != nil {
		t.Fatal(err)
	}
	in := sessionInput(t)
	r, err := Session(in, []SkillRoot{{Name: "release", Dir: release}})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join("..", "..", "skills", "testdata", "digests", "*.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		hex, name, _ := strings.Cut(strings.TrimSuffix(string(data), "\n"), "  ")
		want[name] = "sha256:" + hex
	}
	if len(r.Manifest.Skills) != len(want) {
		t.Fatalf("rendered %d skills, release has %d", len(r.Manifest.Skills), len(want))
	}
	for _, s := range r.Manifest.Skills {
		if s.Digest != want[s.Name] || s.Root != "release" || s.Path != ".claude/skills/"+s.Name {
			t.Errorf("skill %s = %s from %s at %s; release digest %s", s.Name, s.Digest, s.Root, s.Path, want[s.Name])
		}
	}
}

func TestSessionRoleDigestStableAcrossLineEndings(t *testing.T) {
	in := sessionInput(t)
	want, err := Session(in, roots())
	if err != nil {
		t.Fatal(err)
	}
	for _, eol := range []string{"\r\n", "\r"} {
		in := sessionInput(t)
		in.Role = writeVariant(t, t.TempDir(), in.Role, eol)
		in.Role.Root = "copy"
		got, err := Session(in, roots())
		if err != nil {
			t.Fatal(err)
		}
		if got.Manifest.Role != want.Manifest.Role {
			t.Errorf("%q role = %+v, want %+v", eol, got.Manifest.Role, want.Manifest.Role)
		}
		if b, err := os.ReadFile(got.RolePath); err != nil || strings.Contains(string(b), "\r") {
			t.Errorf("%q role file keeps CR: %q, %v", eol, b, err)
		}
	}
}

func TestSessionFirstRootWinsAndOrdersByName(t *testing.T) {
	r, err := Session(sessionInput(t), roots())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
	prev := ""
	for _, s := range r.Manifest.Skills {
		if s.Name <= prev {
			t.Errorf("skills out of order: %s after %s", s.Name, prev)
		}
		prev, names[s.Name] = s.Name, s.Root
	}
	release, err := os.ReadDir(filepath.Join("testdata", "skills", "release"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range release {
		if e.IsDir() && names[e.Name()] != "release" {
			t.Errorf("release skill %s came from %q", e.Name(), names[e.Name()])
		}
	}
}

func TestSessionRefusals(t *testing.T) {
	cases := map[string]func(in *SessionInput){
		"non-empty dir": func(in *SessionInput) {
			if err := os.MkdirAll(in.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(in.Dir, "left.txt"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing role":            func(in *SessionInput) { in.Role.Path = filepath.Join(t.TempDir(), "none.md") },
		"absolute runtime file":   func(in *SessionInput) { in.RuntimeFiles = map[string][]byte{"/etc/x": nil} },
		"escaping runtime file":   func(in *SessionInput) { in.RuntimeFiles = map[string][]byte{"../x": nil} },
		"runtime file on role":    func(in *SessionInput) { in.RuntimeFiles = map[string][]byte{RoleFile: nil} },
		"runtime file in skills":  func(in *SessionInput) { in.RuntimeFiles = map[string][]byte{".claude/skills/x": nil} },
		"escaping skills dir":     func(in *SessionInput) { in.SkillsDir = "../skills" },
		"number":                  func(in *SessionInput) { in.Number = 0 },
		"multi-line item":         func(in *SessionInput) { in.InFlight = []string{"one\ntwo"} },
		"multi-line parked item":  func(in *SessionInput) { in.Parked = []string{"one\rtwo"} },
		"multi-line running item": func(in *SessionInput) { in.Running = []string{"one\ntwo"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := sessionInput(t)
			mutate(&in)
			if _, err := Session(in, roots()); err == nil {
				t.Fatal("rendered")
			}
			if name != "non-empty dir" {
				assertAbsentOrEmpty(t, in.Dir)
			}
		})
	}
	in := sessionInput(t)
	if err := os.MkdirAll(in.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in.Dir, "left.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Session(in, roots()); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("non-empty dir: err = %v, want ErrNotEmpty", err)
	}
}
