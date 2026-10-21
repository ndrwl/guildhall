package skills_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
	"github.com/ndrwl/guildhall/skills"
)

var update = flag.Bool("update", false, "rewrite the digest golden")

// roleEnv makes the test binary act as go or gofmt in a child process, named by its executable.
const roleEnv = "GUILDHALL_SKILLS_TEST_ROLE"

func TestMain(m *testing.M) {
	if os.Getenv(roleEnv) == "tool" {
		os.Exit(fakeTool(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeTool logs one line per call to FAKE_TOOL_LOG: the tool name, its arguments and the download settings it saw.
// It exits 3 when FAKE_TOOL_FAIL names the tool and its first argument, and gofmt prints FAKE_GOFMT_LIST.
func fakeTool(args []string) int {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	call := strings.Join(append([]string{name}, args...), " ")
	f, err := os.OpenFile(os.Getenv("FAKE_TOOL_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	_, err = fmt.Fprintf(f, "%s GOPROXY=%s GOTOOLCHAIN=%s\n", call, os.Getenv("GOPROXY"), os.Getenv("GOTOOLCHAIN"))
	if err = errors.Join(err, f.Close()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if len(args) > 0 && os.Getenv("FAKE_TOOL_FAIL") == name+" "+args[0] {
		return 3
	}
	if name == "gofmt" {
		fmt.Print(os.Getenv("FAKE_GOFMT_LIST"))
	}
	return 0
}

// maxDescription caps a skill's description, the only part a runtime loads before the skill fires.
const maxDescription = 256

const goldenPath = "testdata/digests.txt"

// shipped lists the skill directories on disk: every directory of the root except testdata.
func shipped(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && e.Name() != "testdata" {
			names = append(names, e.Name())
		}
	}
	return names
}

// treeDigest is the sha256 of the sha256sum listing of a skill's sorted files, as docs/rendering.md defines it.
func treeDigest(t *testing.T, fsys fs.FS, name string) string {
	t.Helper()
	var files []string
	err := fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, strings.TrimPrefix(p, name+"/"))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	var listing strings.Builder
	for _, f := range files {
		data, err := fs.ReadFile(fsys, name+"/"+f)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&listing, "%x  %s\n", sum, f)
	}
	sum := sha256.Sum256([]byte(listing.String()))
	return hex.EncodeToString(sum[:])
}

// goldenDigests reads testdata/digests.txt: one "<hex>  <skill>" line per skill, recorded with sha256sum.
func goldenDigests(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for line := range strings.Lines(string(data)) {
		hexsum, name, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "  ")
		if !ok {
			t.Fatalf("%s: malformed line %q", goldenPath, line)
		}
		want[name] = hexsum
	}
	return want
}

func TestReleaseHoldsEveryShippedSkill(t *testing.T) {
	var embedded []string
	entries, err := fs.ReadDir(skills.Release(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			embedded = append(embedded, e.Name())
		}
	}
	want := slices.Sorted(maps.Keys(goldenDigests(t)))
	if got := shipped(t); !slices.Equal(got, want) {
		t.Errorf("skill directories = %q, golden = %q", got, want)
	}
	if !slices.Equal(embedded, want) {
		t.Errorf("embedded skills = %q, golden = %q", embedded, want)
	}
	if _, err := fs.Stat(skills.Release(), "CREDITS.md"); err != nil {
		t.Errorf("release root has no CREDITS.md: %v", err)
	}
}

func TestTreeDigestsMatchGolden(t *testing.T) {
	want := goldenDigests(t)
	if *update {
		var out strings.Builder
		for _, name := range shipped(t) {
			fmt.Fprintf(&out, "%s  %s\n", treeDigest(t, skills.Release(), name), name)
		}
		if err := os.WriteFile(goldenPath, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		want = goldenDigests(t)
	}
	for name, hexsum := range want {
		if got := treeDigest(t, skills.Release(), name); got != hexsum {
			t.Errorf("%s: tree digest %s, golden %s (a CRLF checkout changes it: see .gitattributes)", name, got, hexsum)
		}
	}
}

// frontmatter parses the "---" block a SKILL.md starts with: one "key: value" line per field.
func frontmatter(text string) (map[string]string, error) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, errors.New("does not start with a --- line")
	}
	block, _, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return nil, errors.New("has no closing --- line")
	}
	fields := map[string]string{}
	for line := range strings.Lines(block) {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), ": ")
		switch {
		case !ok || strings.TrimSpace(value) == "":
			return nil, fmt.Errorf("line %q is not key: value", line)
		case key != "name" && key != "description":
			return nil, fmt.Errorf("unknown field %q", key)
		case fields[key] != "":
			return nil, fmt.Errorf("field %q repeated", key)
		}
		fields[key] = value
	}
	if fields["name"] == "" || fields["description"] == "" {
		return nil, errors.New("needs a name and a description")
	}
	return fields, nil
}

func TestSkillFrontmatter(t *testing.T) {
	for _, name := range shipped(t) {
		t.Run(name, func(t *testing.T) {
			data, err := fs.ReadFile(skills.Release(), name+"/SKILL.md")
			if err != nil {
				t.Fatal(err)
			}
			fields, err := frontmatter(string(data))
			if err != nil {
				t.Fatalf("SKILL.md frontmatter %v", err)
			}
			if fields["name"] != name {
				t.Errorf("name %q, directory %q", fields["name"], name)
			}
			if !strings.HasPrefix(name, "guildhall-") || !workflow.ValidID(name) {
				t.Errorf("name %q needs the guildhall- prefix and a valid skill id", name)
			}
			if n := len(fields["description"]); n > maxDescription {
				t.Errorf("description is %d bytes, cap %d", n, maxDescription)
			}
		})
	}
}

func TestRegularFilesWithLFOnly(t *testing.T) {
	for _, name := range append(shipped(t), "CREDITS.md") {
		err := filepath.WalkDir(name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !d.Type().IsRegular() {
				t.Errorf("%s is not a regular file", filepath.ToSlash(p))
				return nil
			}
			data, err := os.ReadFile(p)
			if bytes.Contains(data, []byte("\r")) {
				t.Errorf("%s has a carriage return: check out skills with LF endings (.gitattributes)", filepath.ToSlash(p))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// sentenceBreak finds a sentence end followed by another sentence on the same line.
var sentenceBreak = regexp.MustCompile("[.!?][\"')`*_]*\\s+[\"'(`*_]*[A-Z]")

// listMarker is a Markdown list item's marker, whose "1." is not a sentence end.
var listMarker = regexp.MustCompile(`^\s*(\d+\.|[-*])\s+`)

func TestOneSentencePerLine(t *testing.T) {
	err := fs.WalkDir(skills.Release(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".md" {
			return err
		}
		data, err := fs.ReadFile(skills.Release(), p)
		if err != nil {
			return err
		}
		text := string(data)
		if _, body, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n"); ok && strings.HasPrefix(text, "---\n") {
			text = body
		}
		fenced := false
		for i, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				fenced = !fenced
				continue
			}
			if !fenced && sentenceBreak.MatchString(listMarker.ReplaceAllString(line, "")) {
				t.Errorf("%s: body line %d has more than one sentence: %q", p, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestScriptsAreTwinnedAndParse(t *testing.T) {
	err := fs.WalkDir(skills.Release(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Base(path.Dir(p)) != "scripts" {
			return err
		}
		ext := path.Ext(p)
		twin := map[string]string{".sh": ".ps1", ".ps1": ".sh"}[ext]
		if twin == "" {
			t.Errorf("%s: a script is a .sh or a .ps1", p)
			return nil
		}
		if _, err := fs.Stat(skills.Release(), strings.TrimSuffix(p, ext)+twin); err != nil {
			t.Errorf("%s has no %s twin", p, twin)
		}
		t.Run(p, func(t *testing.T) { parses(t, filepath.FromSlash(p)) })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// parses checks a script's syntax in its own shell without running it, skipping a shell that is not installed.
func parses(t *testing.T, file string) {
	shell, args := "sh", []string{"-n", file}
	if filepath.Ext(file) == ".ps1" {
		quoted := "'" + strings.ReplaceAll(file, "'", "''") + "'"
		shell, args = "pwsh", []string{"-NoProfile", "-NonInteractive", "-Command",
			"$e = $null; [void][System.Management.Automation.Language.Parser]::ParseFile(" + quoted +
				", [ref]$null, [ref]$e); if ($e) { $e | Out-String; exit 1 }"}
	}
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s is not installed", shell)
	}
	if out, err := exec.Command(shell, args...).CombinedOutput(); err != nil {
		t.Errorf("%s does not parse in %s: %v\n%s", file, shell, err, out)
	}
}

// TestContractTDDCheckTwinsAgree runs both installed check scripts against the test binary acting as go and gofmt:
// each makes the same calls with downloads off and reaches the same pass or fail decision.
func TestContractTDDCheckTwinsAgree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "release")
	if err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, tool := range []string{"go", "gofmt"} {
		if runtime.GOOS == "windows" {
			tool += ".exe"
		}
		copyFile(t, self, filepath.Join(bin, tool))
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(roleEnv, "tool")
	t.Setenv("GOPROXY", "https://proxy.golang.org")
	t.Setenv("GOTOOLCHAIN", "auto")

	const env = " GOPROXY=off GOTOOLCHAIN=local"
	gofmt, vet, test := "gofmt -l ."+env, "go vet ./..."+env, "go test ./..."+env
	cases := []struct {
		name, fail, list string
		calls            []string
		ok               bool
	}{
		{name: "clean", calls: []string{gofmt, vet, test}, ok: true},
		{name: "unformatted file", list: "bad.go\n", calls: []string{gofmt}},
		{name: "gofmt fails", fail: "gofmt -l", calls: []string{gofmt}},
		{name: "vet fails", fail: "go vet", calls: []string{gofmt, vet}},
		{name: "test fails", fail: "go test", calls: []string{gofmt, vet, test}},
	}
	scripts := map[string][]string{
		"sh":   {filepath.Join(root, "guildhall-contract-tdd", "scripts", "check.sh")},
		"pwsh": {"-NoProfile", "-NonInteractive", "-File", filepath.Join(root, "guildhall-contract-tdd", "scripts", "check.ps1")},
	}
	for _, shell := range []string{"sh", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s is not installed", shell)
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					log := filepath.Join(t.TempDir(), "calls")
					t.Setenv("FAKE_TOOL_LOG", log)
					t.Setenv("FAKE_TOOL_FAIL", c.fail)
					t.Setenv("FAKE_GOFMT_LIST", c.list)
					cmd := exec.Command(shell, scripts[shell]...)
					cmd.Dir = t.TempDir()
					out, err := cmd.CombinedOutput()
					if ok := err == nil; ok != c.ok {
						t.Errorf("passed = %t, want %t: %v\n%s", ok, c.ok, err, out)
					}
					if c.list != "" && !strings.Contains(string(out), strings.TrimSpace(c.list)) {
						t.Errorf("output does not name %q:\n%s", c.list, out)
					}
					data, err := os.ReadFile(log)
					if err != nil && !errors.Is(err, fs.ErrNotExist) {
						t.Fatal(err)
					}
					if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !slices.Equal(got, c.calls) {
						t.Errorf("calls = %q, want %q\n%s", got, c.calls, out)
					}
				})
			}
		})
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallIsTheFirstRenderRoot(t *testing.T) {
	tmp := t.TempDir()
	release := filepath.Join(tmp, "release")
	if err := skills.Install(release); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(release, "CREDITS.md")); err != nil {
		t.Errorf("installed root has no CREDITS.md: %v", err)
	}
	managed := filepath.Join(tmp, "managed")
	names := shipped(t)
	for _, name := range names {
		decoy := filepath.Join(managed, name)
		if err := os.MkdirAll(decoy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(decoy, "SKILL.md"), []byte("managed copy\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"role.md", "guide.md"} {
		if err := os.WriteFile(filepath.Join(tmp, f), []byte(f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	step := workflow.Step{ID: "grill", Type: "grill", Version: 1, Settings: map[string]json.RawMessage{"actor": json.RawMessage(`"artisan"`), "skills": list}}
	if err := workflow.ValidateStepSettings(step); err != nil {
		t.Fatal(err)
	}
	r, err := render.Prompt(
		render.Stage{Repository: "cli", Task: "t1", Step: step, Attempt: 1, Dir: filepath.Join(tmp, "attempt")},
		nil,
		render.Baseline{Role: render.Source{Name: "role.md", Path: filepath.Join(tmp, "role.md")},
			Guide: render.Source{Name: "guide.md", Path: filepath.Join(tmp, "guide.md")}},
		[]render.SkillRoot{{Name: "release", Dir: release}, {Name: "managed", Dir: managed}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := goldenDigests(t)
	for _, s := range r.Manifest.Skills {
		for _, dir := range []string{release, r.SkillsPath} {
			notice, err := os.ReadFile(filepath.Join(dir, s.Name, "CREDITS.md"))
			if err != nil {
				t.Fatal(err)
			}
			wantNotice, err := fs.ReadFile(skills.Release(), "CREDITS.md")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(notice, wantNotice) {
				t.Errorf("%s: MIT notice not preserved", dir)
			}
		}
		if s.Root != "release" || s.Digest != "sha256:"+want[s.Name] {
			t.Errorf("%s rendered from %s with %s, want release with golden %s", s.Name, s.Root, s.Digest, want[s.Name])
		}
	}
	if len(r.Manifest.Skills) != len(names) {
		t.Errorf("rendered %d skills, want %d", len(r.Manifest.Skills), len(names))
	}
}

func TestInstallRefusesANonEmptyDirectory(t *testing.T) {
	empty := t.TempDir()
	if err := skills.Install(empty); err != nil {
		t.Fatalf("empty directory: %v", err)
	}
	if err := skills.Install(empty); !errors.Is(err, skills.ErrNotEmpty) {
		t.Fatalf("second install = %v, want ErrNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(empty, "CREDITS.md")); err != nil {
		t.Errorf("refused install removed the first: %v", err)
	}
}
