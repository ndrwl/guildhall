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
	"testing/fstest"

	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/testenv"
	"github.com/ndrwl/guildhall/internal/workflow"
	"github.com/ndrwl/guildhall/skills"
)

var update = flag.Bool("update", false, "rewrite the digest goldens")

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

// goldenDir holds one sha256sum-format <skill>.sha256 file per shipped skill.
const goldenDir = "testdata/digests"

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

// hexDigest is a sha256 as sha256sum prints it.
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readGolden reads a golden directory: one <skill>.sha256 file per skill, holding its "<hex>  <skill>" sha256sum line.
// It does not trim a CR, so a CRLF checkout fails here rather than passing unnoticed.
func readGolden(fsys fs.FS) (map[string]string, error) {
	files, err := fs.Glob(fsys, "*.sha256")
	if err != nil {
		return nil, err
	}
	want := map[string]string{}
	for _, file := range files {
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return nil, err
		}
		line, ok := strings.CutSuffix(string(data), "\n")
		hexsum, name, cut := strings.Cut(line, "  ")
		if !ok || !cut || strings.Contains(line, "\n") || !hexDigest.MatchString(hexsum) {
			return nil, fmt.Errorf("%s: want one %q line, got %q", file, "<hex>  <skill>", data)
		}
		if skill := strings.TrimSuffix(file, ".sha256"); name != skill {
			return nil, fmt.Errorf("%s: records skill %q, want %q", file, name, skill)
		}
		want[name] = hexsum
	}
	return want, nil
}

// checkSkills compares the golden's skills with the shipped ones: a skill with no file and an orphan file each fail.
func checkSkills(golden map[string]string, names []string) error {
	var errs []error
	for _, name := range names {
		if _, ok := golden[name]; !ok {
			errs = append(errs, fmt.Errorf("skill %s has no golden %s.sha256", name, name))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(golden)) {
		if !slices.Contains(names, name) {
			errs = append(errs, fmt.Errorf("golden %s.sha256 names no shipped skill", name))
		}
	}
	return errors.Join(errs...)
}

// checkGolden compares the golden directory fsys with each skill's tree digest in got.
func checkGolden(fsys fs.FS, got map[string]string) error {
	golden, err := readGolden(fsys)
	if err != nil {
		return err
	}
	errs := []error{checkSkills(golden, slices.Sorted(maps.Keys(got)))}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if want, ok := golden[name]; ok && got[name] != want {
			errs = append(errs, fmt.Errorf("%s: tree digest %s, golden %s (a CRLF checkout changes it: see .gitattributes)",
				name, got[name], want))
		}
	}
	return errors.Join(errs...)
}

// writeGolden writes one <skill>.sha256 file per skill of got into dir and removes any orphan .sha256 file.
func writeGolden(dir string, got map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, hexsum := range got {
		if err := os.WriteFile(filepath.Join(dir, name+".sha256"), fmt.Appendf(nil, "%s  %s\n", hexsum, name), 0o644); err != nil {
			return err
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.sha256"))
	if err != nil {
		return err
	}
	for _, file := range files {
		if _, ok := got[strings.TrimSuffix(filepath.Base(file), ".sha256")]; !ok {
			if err := os.Remove(file); err != nil {
				return err
			}
		}
	}
	return nil
}

// goldenDigests reads the release's golden directory, failing the test when it is malformed.
func goldenDigests(t *testing.T) map[string]string {
	t.Helper()
	want, err := readGolden(os.DirFS(goldenDir))
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// releaseDigests is each shipped skill's tree digest in the release.
func releaseDigests(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, name := range shipped(t) {
		got[name] = treeDigest(t, skills.Release(), name)
	}
	return got
}

func TestGoldenRefusesAMissingOrphanOrMalformedFile(t *testing.T) {
	const a, b = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	good := fstest.MapFS{"one.sha256": file(a + "  one\n"), "two.sha256": file(b + "  two\n")}
	got := map[string]string{"one": a, "two": b}
	if err := checkGolden(good, got); err != nil {
		t.Fatalf("matching golden: %v", err)
	}
	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		got  map[string]string
		want string
	}{
		{"missing file", fstest.MapFS{"one.sha256": good["one.sha256"]}, got, "skill two has no golden two.sha256"},
		{"orphan file", good, map[string]string{"one": a}, "golden two.sha256 names no shipped skill"},
		{"two lines", fstest.MapFS{"one.sha256": file(a + "  one\n" + a + "  one\n")}, got, "one.sha256: want one"},
		{"no separator", fstest.MapFS{"one.sha256": file(a + " one\n")}, got, "one.sha256: want one"},
		{"bad hex", fstest.MapFS{"one.sha256": file("xyz  one\n")}, got, "one.sha256: want one"},
		{"no newline", fstest.MapFS{"one.sha256": file(a + "  one")}, got, "one.sha256: want one"},
		{"CRLF", fstest.MapFS{"one.sha256": file(a + "  one\r\n")}, got, `one.sha256: records skill "one\r"`},
		{"other name", fstest.MapFS{"one.sha256": file(a + "  two\n")}, got, `one.sha256: records skill "two", want "one"`},
		{"other digest", good, map[string]string{"one": b, "two": b}, "one: tree digest " + b + ", golden " + a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkGolden(tc.fsys, tc.got); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("checkGolden = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWriteGoldenRemovesAnOrphan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gone.sha256"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{"one": strings.Repeat("a", 64)}
	if err := writeGolden(dir, got); err != nil {
		t.Fatal(err)
	}
	if err := checkGolden(os.DirFS(dir), got); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.sha256")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("orphan kept: %v", err)
	}
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
	if err := checkSkills(goldenDigests(t), shipped(t)); err != nil {
		t.Error(err)
	}
	if got := shipped(t); !slices.Equal(embedded, got) {
		t.Errorf("embedded skills = %q, skill directories = %q", embedded, got)
	}
	if _, err := fs.Stat(skills.Release(), "CREDITS.md"); err != nil {
		t.Errorf("release root has no CREDITS.md: %v", err)
	}
}

func TestTreeDigestsMatchGolden(t *testing.T) {
	got := releaseDigests(t)
	if *update {
		if err := writeGolden(goldenDir, got); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkGolden(os.DirFS(goldenDir), got); err != nil {
		t.Error(err)
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
		value = strings.TrimSpace(value)
		if strings.ContainsAny(value[:1], "-?:,[]{}#&*!|>'\"%@`") ||
			strings.Contains(value, ": ") || strings.Contains(value, ":\t") || strings.HasSuffix(value, ":") ||
			strings.Contains(value, " #") || strings.Contains(value, "\t#") {
			return nil, fmt.Errorf("field %q must be a plain YAML scalar without indicators, mapping separators or comments", key)
		}
		fields[key] = value
	}
	if fields["name"] == "" || fields["description"] == "" {
		return nil, errors.New("needs a name and a description")
	}
	return fields, nil
}

func TestFrontmatterPlainScalars(t *testing.T) {
	for _, field := range []string{"name", "description"} {
		values := []string{"contract first: state", "contract first:\tstate", "contract:", "text # comment", "text\t# comment"}
		for _, indicator := range "-?:,[]{}#&*!|>'\"%@`" {
			values = append(values, string(indicator)+"value")
		}
		for _, value := range values {
			t.Run(field+"/"+value, func(t *testing.T) {
				fields := map[string]string{"name": "guildhall-example", "description": "Use for contract tests."}
				fields[field] = value
				text := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n", fields["name"], fields["description"])
				if got, err := frontmatter(text); err == nil {
					t.Fatalf("frontmatter accepted unsupported scalar %q: %v", value, got)
				}
			})
		}
	}
	t.Run("plain strings", func(t *testing.T) {
		got, err := frontmatter("---\nname: guildhall-example\ndescription: Use https://example.com for contract-first tests.\n---\n")
		if err != nil {
			t.Fatal(err)
		}
		if got["name"] != "guildhall-example" || got["description"] != "Use https://example.com for contract-first tests." {
			t.Fatalf("frontmatter = %v", got)
		}
	})
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

// TestAdaptedSkillsCarryCredits checks that a skill carries CREDITS.md exactly when its body names an upstream source.
func TestAdaptedSkillsCarryCredits(t *testing.T) {
	notice, err := fs.ReadFile(skills.Release(), "CREDITS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range shipped(t) {
		data, err := fs.ReadFile(skills.Release(), name+"/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		adapted := strings.Contains(string(data), "\nAdapted from `")
		credits, err := fs.ReadFile(skills.Release(), name+"/CREDITS.md")
		switch {
		case adapted && !bytes.Equal(credits, notice):
			t.Errorf("%s is adapted but its CREDITS.md is not the release's: %v", name, err)
		case adapted && !strings.Contains(string(notice), "| `"+name+"` |"):
			t.Errorf("%s is adapted but CREDITS.md does not list its upstream source", name)
		case !adapted && err == nil:
			t.Errorf("%s carries CREDITS.md but names no upstream source", name)
		}
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

// parses checks a script's syntax in its own shell without running it, skipping a shell that is not installed unless the runner requires it.
func parses(t *testing.T, file string) {
	shell, args := "sh", []string{"-n", file}
	if filepath.Ext(file) == ".ps1" {
		quoted := "'" + strings.ReplaceAll(file, "'", "''") + "'"
		shell, args = "pwsh", []string{"-NoProfile", "-NonInteractive", "-Command",
			"$e = $null; [void][System.Management.Automation.Language.Parser]::ParseFile(" + quoted +
				", [ref]$null, [ref]$e); if ($e) { $e | Out-String; exit 1 }"}
	}
	if _, err := exec.LookPath(shell); err != nil {
		testenv.SkipUnlessRequired(t, shell, "%s is not installed", shell)
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
				testenv.SkipUnlessRequired(t, shell, "%s is not installed", shell)
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

// TestSelfReviewFactsTwinsAgree runs both installed facts scripts on a fixture diff, with no model and no tool but
// the shell: each prints the golden facts, flags the size budget it is given, and refuses a missing diff or a
// budget that is not a whole number.
func TestSelfReviewFactsTwinsAgree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "release")
	if err := skills.Install(root); err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join("testdata", "self-review", "fixture.diff"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "self-review", "fixture.facts"))
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(t.TempDir(), "empty.diff")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// budget rewrites the golden's size lines for the budget given.
	budget := func(lines, files, over string) string {
		return strings.Replace(string(golden), "size_budget_lines: 400\nsize_budget_files: 15\nsize_over: no\n",
			"size_budget_lines: "+lines+"\nsize_budget_files: "+files+"\nsize_over: "+over+"\n", 1)
	}
	cases := []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{name: "default budget", args: []string{fixture}, want: string(golden), ok: true},
		{name: "at the budget", args: []string{fixture, "21", "8"}, want: budget("21", "8", "no"), ok: true},
		{name: "over the line budget", args: []string{fixture, "20", "0"}, want: budget("20", "0", "yes"), ok: true},
		{name: "over the file budget", args: []string{fixture, "0", "7"}, want: budget("0", "7", "yes"), ok: true},
		{name: "budget off", args: []string{fixture, "0", "0"}, want: budget("0", "0", "no"), ok: true},
		{name: "empty diff", args: []string{empty}, ok: true, want: "files: 0\ntest_files: 0\nnontest_lines: 0\nsize_budget_lines: 400\n" +
			"size_budget_files: 15\nsize_over: no\nsleeps_added: 0\nsleeps_removed: 0\nskips_added: 0\nskips_removed: 0\n" +
			"assertions_removed: 0\nsecrets: 0\nworkflow_problems: 0\n"},
		{name: "missing diff", args: []string{filepath.Join(t.TempDir(), "absent.diff")}},
		{name: "no diff", args: nil},
		{name: "budget not a number", args: []string{fixture, "many"}},
		{name: "negative budget", args: []string{fixture, "400", "-1"}},
	}
	scripts := map[string][]string{
		"sh":   {filepath.Join(root, "guildhall-self-review", "scripts", "facts.sh")},
		"pwsh": {"-NoProfile", "-NonInteractive", "-File", filepath.Join(root, "guildhall-self-review", "scripts", "facts.ps1")},
	}
	for _, shell := range []string{"sh", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				testenv.SkipUnlessRequired(t, shell, "%s is not installed", shell)
			}
			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					var stdout, stderr bytes.Buffer
					cmd := exec.Command(shell, append(slices.Clone(scripts[shell]), c.args...)...)
					cmd.Dir, cmd.Stdout, cmd.Stderr = t.TempDir(), &stdout, &stderr
					err := cmd.Run()
					if ok := err == nil; ok != c.ok {
						t.Fatalf("passed = %t, want %t: %v\n%s%s", ok, c.ok, err, &stdout, &stderr)
					}
					if !c.ok {
						if stderr.Len() == 0 {
							t.Errorf("a refusal says nothing on stderr")
						}
						return
					}
					if got := strings.ReplaceAll(stdout.String(), "\r\n", "\n"); got != c.want {
						t.Errorf("facts:\n%s\nwant:\n%s\nstderr: %s", got, c.want, &stderr)
					}
				})
			}
		})
	}
}

// TestSelfReviewSkillDocumentShape checks the skill's example document: its schema, facts named as the facts
// script prints them, and criteria each with the field its kind records.
func TestSelfReviewSkillDocumentShape(t *testing.T) {
	data, err := fs.ReadFile(skills.Release(), "guildhall-self-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, example, ok := strings.Cut(string(data), "```json\n")
	example, _, closed := strings.Cut(example, "```")
	if !ok || !closed {
		t.Fatal("SKILL.md has no fenced json example document")
	}
	var doc struct {
		Schema   string                     `json:"schema"`
		Settings map[string]int             `json:"settings"`
		Facts    map[string]json.RawMessage `json:"facts"`
		Criteria []struct {
			ID, Kind, Judge, Evidence string
			Pass                      *bool
			Score                     *int
			Value                     *float64
		} `json:"criteria"`
	}
	dec := json.NewDecoder(strings.NewReader(example))
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("example document: %v", err)
	}
	if doc.Schema != "guildhall.self-review/1" {
		t.Errorf("schema = %q", doc.Schema)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "self-review", "fixture.facts"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"items"}
	for line := range strings.Lines(string(golden)) {
		if key, _, ok := strings.Cut(line, ": "); ok && !strings.HasPrefix(key, "size_budget_") {
			want = append(want, key)
		}
	}
	if got := slices.Sorted(maps.Keys(doc.Facts)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("facts keys = %q, want the facts script's counts and items %q", got, want)
	}
	if got := slices.Sorted(maps.Keys(doc.Settings)); !slices.Equal(got, []string{"size_budget_files", "size_budget_lines"}) {
		t.Errorf("settings keys = %q", got)
	}
	for _, c := range doc.Criteria {
		if !slices.Contains([]string{"script", "model", "guildmaster", "pending"}, c.Judge) {
			t.Errorf("criterion %s: judge %q", c.ID, c.Judge)
		}
		switch {
		case c.Kind == "gate" && c.Pass == nil && c.Evidence == "", c.Kind == "trend" && c.Value == nil:
			t.Errorf("criterion %s: a %s needs its pass, what is missing, or its value", c.ID, c.Kind)
		case c.Kind == "score" && c.Judge == "pending" && c.Score != nil:
			t.Errorf("criterion %s: a pending score has no score", c.ID)
		case c.Kind == "score" && c.Judge != "pending" && (c.Score == nil || *c.Score < 0 || *c.Score > 3):
			t.Errorf("criterion %s: a judged score is 0 to 3", c.ID)
		case !slices.Contains([]string{"gate", "score", "trend"}, c.Kind):
			t.Errorf("criterion %s: kind %q", c.ID, c.Kind)
		}
	}
}

func TestReviewSkillSpeaksTheOutcomeContract(t *testing.T) {
	const name = "guildhall-review"
	var files []string
	err := fs.WalkDir(skills.Release(), name, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, strings.TrimPrefix(p, name+"/"))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"CREDITS.md", "SKILL.md", "smells.md"}; !slices.Equal(files, want) {
		t.Errorf("files = %q, want %q: no scripts and no sub-agent definitions", files, want)
	}
	data, err := fs.ReadFile(skills.Release(), name+"/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "guildhall review check review-outcome.json") {
		t.Error("SKILL.md does not require the guildhall review check self-check")
	}

	_, example, ok := strings.Cut(text, "```json\n")
	example, _, closed := strings.Cut(example, "```")
	if !ok || !closed {
		t.Fatal("SKILL.md has no fenced json example outcome")
	}
	o, err := workflow.ParseReviewOutcome([]byte(example))
	if err != nil {
		t.Fatalf("example outcome: %v", err)
	}
	if o.Schema != workflow.ReviewOutcomeSchema || o.Summary == "" || !o.Blocking() || len(o.Suggestions) == 0 ||
		!slices.ContainsFunc(o.Suggestions, func(s workflow.Suggestion) bool { return strings.HasPrefix(s.ID, "smell-") }) {
		t.Errorf("example should be a /2 blocking round with a summary and a smell- suggestion: %+v", o)
	}
	for _, state := range []workflow.DispositionState{workflow.Resolved, workflow.Unresolved, workflow.Disputed} {
		if !slices.ContainsFunc(o.Dispositions, func(d workflow.Disposition) bool { return d.State == state }) {
			t.Errorf("example shows no %s disposition: %+v", state, o.Dispositions)
		}
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
		if _, err := fs.Stat(skills.Release(), s.Name+"/CREDITS.md"); errors.Is(err, fs.ErrNotExist) {
			continue // not adapted; TestAdaptedSkillsCarryCredits checks which skills are
		}
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

// directorSkills are the skills the Director's session works through (docs/decisions/director-conversation-first.md: plain English is the primary interface).
var directorSkills = []string{
	"guildhall-director-inbox",
	"guildhall-director-stage",
	"guildhall-director-startup",
	"guildhall-director-tasks",
	"guildhall-handoff",
	"guildhall-maintenance",
}

// notAskedOfTheDirector lists the README commands the Guildmaster never asks the Director to run, with why.
var notAskedOfTheDirector = map[string]string{
	"help":                     "the Director answers in plain English",
	"store init":               "a store exists before any Director runs",
	"director start":           "the Guildmaster starts the Director; a session cannot start itself",
	"artisan start":            "task start and step retry launch every attempt",
	"fake-artisan":             "a test wrapper artisan start runs",
	"claude-artisan":           "the Claude wrapper artisan start runs",
	"steward rollout":          "the Steward's command, not the Director's",
	"steward start":            "the Guildmaster starts the Steward session; no Director runs it",
	"steward status":           "the Steward's command, not the Director's",
	"steward approval request": "the Steward's command, not the Director's",
	"render":                   "the engine renders every prompt",
	"director turn-started":    "the runtime's hook runs it when a turn starts",
	"director turn-ended":      "the runtime's hook runs it when a turn ends",
	"director context-report":  "the runtime's status line runs it after each message",
	"quality judged":           "a judge attempt's launcher runs it when the attempt ends",
	"ci log":                   "Artisans run it from a failed validate result's logs[].command",
	"clerk guild":              "the Clerk plugin's /guild runs it for the Guildmaster",
	"clerk decisions":          "the Clerk plugin's /decisions runs it for the Guildmaster",
	"clerk task":               "the Clerk plugin's /task runs it for the Guildmaster",
	"clerk inspect":            "the Clerk plugin's /inspect runs it for the Guildmaster",
	"clerk queue":              "the Clerk plugin's /queue runs it for the Guildmaster",
	"web":                      "the Director's session serves the task pages itself; the Guildmaster runs it by hand when none runs",
}

// readmeCommand finds each `guildhall <command>` in the README's command list.
var readmeCommand = regexp.MustCompile("`guildhall ([^`]+)`")

// skillCommand finds a guildhall command a skill names in code: its words up to the first flag or placeholder.
var skillCommand = regexp.MustCompile("(?m)(?:`|^[ \t]*)guildhall ([a-z][a-z-]*(?: [a-z][a-z-]*)*)")

// commandNames returns the commands a usage phrase names: its words before any flag, placeholder or ellipsis,
// with an alternation such as "task new|show" expanded to one command per choice.
func commandNames(phrase string) []string {
	var words []string
	for _, w := range strings.Fields(phrase) {
		if strings.ContainsAny(w[:1], "-<[(.") {
			break
		}
		words = append(words, w)
	}
	i := slices.IndexFunc(words, func(w string) bool { return strings.Contains(w, "|") })
	if i < 0 {
		return []string{strings.Join(words, " ")}
	}
	var names []string
	for _, choice := range strings.Split(strings.Join(words[i:], " "), "|") {
		names = append(names, strings.Join(append(slices.Clone(words[:i]), choice), " "))
	}
	return names
}

// TestDirectorSkillsNameEveryReadmeCommand is the conversation-first decision's rule (docs/decisions/director-conversation-first.md), mechanically checked: every command of the README's
// command list that the Guildmaster would otherwise type is named by at least one of the Director's skills.
func TestDirectorSkillsNameEveryReadmeCommand(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, list, ok := strings.Cut(string(readme), "\n## The commands\n")
	if !ok {
		t.Fatal("README.md has no The commands section")
	}
	list, _, _ = strings.Cut(list, "\n## ")
	var commands []string
	for _, m := range readmeCommand.FindAllStringSubmatch(list, -1) {
		commands = append(commands, commandNames(m[1])...)
	}
	if len(commands) == 0 {
		t.Fatal("README.md lists no commands")
	}

	var named []string
	for _, name := range directorSkills {
		err := fs.WalkDir(skills.Release(), name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".md" {
				return err
			}
			data, err := fs.ReadFile(skills.Release(), p)
			for _, m := range skillCommand.FindAllStringSubmatch(string(data), -1) {
				named = append(named, m[1])
			}
			return err
		})
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, c := range commands {
		if _, ok := notAskedOfTheDirector[c]; ok {
			continue
		}
		if !slices.ContainsFunc(named, func(n string) bool { return n == c || strings.HasPrefix(n, c+" ") }) {
			t.Errorf("no Director skill names `guildhall %s`", c)
		}
	}
	for c := range notAskedOfTheDirector {
		if !slices.Contains(commands, c) {
			t.Errorf("%q is excused but the README does not list it", c)
		}
	}
	// The other way round, for the maintenance commands: each one a Director skill names is in the README's list.
	for _, n := range named {
		if strings.HasPrefix(n, "director maintenance") && !slices.ContainsFunc(commands, func(c string) bool { return n == c || strings.HasPrefix(n, c+" ") }) {
			t.Errorf("a Director skill names `guildhall %s`, which the README does not list", n)
		}
	}
}
