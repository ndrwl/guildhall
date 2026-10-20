package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/workflow"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from golden:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func step(t *testing.T, id, kind, settings string) workflow.Step {
	t.Helper()
	s := workflow.Step{ID: id, Type: kind, Version: 1}
	if settings != "" {
		if err := json.Unmarshal([]byte(settings), &s.Settings); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func source(name string) Source {
	return Source{Name: name, Path: filepath.Join("testdata", "baseline", filepath.FromSlash(name))}
}

func baseline(kind string, standards ...string) Baseline {
	b := Baseline{Role: source("roles/artisan.md"), Guide: source("stages/" + kind + ".md")}
	for _, s := range standards {
		b.Standards = append(b.Standards, source(s))
	}
	return b
}

func input(t *testing.T, port, name, file string) Input {
	t.Helper()
	path := filepath.Join("testdata", "docs", file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return Input{Port: port, Name: name, SHA256: hex.EncodeToString(sum[:]), Path: path}
}

func roots() []SkillRoot {
	return []SkillRoot{
		{Name: "release", Dir: filepath.Join("testdata", "skills", "release")},
		{Name: "managed", Dir: filepath.Join("testdata", "skills", "managed")},
		{Name: "director", Dir: filepath.Join("testdata", "skills", "absent")},
	}
}

func attempt(t *testing.T, s workflow.Step) Stage {
	return Stage{Repository: "cli", Task: "t7", Step: s, Attempt: 2, Dir: filepath.Join(t.TempDir(), "attempt")}
}

func TestPromptGoldenPerKind(t *testing.T) {
	cases := []struct {
		kind     string
		step     workflow.Step
		inputs   []Input
		baseline Baseline
	}{
		{"grill", step(t, "grill", "grill", `{"actor":"director"}`), nil, baseline("grill")},
		{"research", step(t, "research", "research", ""), nil, baseline("research")},
		{"plan", step(t, "plan", "plan", `{"profile": "planner", "skills": ["guildhall-contributing"]}`),
			[]Input{input(t, "context", "tasks/t7/grill-notes.md", "notes.md")}, baseline("plan")},
		{"build", step(t, "build", "build", `{"profile": "builder", "skills": ["go-tdd", "guildhall-contributing"]}`),
			[]Input{input(t, "spec", "tasks/t7/spec.md", "spec.md")}, baseline("build")},
		{"automated-review", step(t, "review-astra", "review",
			`{"actor":"artisan", "profile": "reviewer-astra", "instructions": "Check the JSON output on Windows too.\r\nCite S1 or S2."}`),
			[]Input{input(t, "spec", "tasks/t7/spec.md", "spec.md")},
			Baseline{Role: source("roles/reviewer.md"), Guide: source("stages/automated-review.md"),
				Standards: []Source{source("standards/review.md")}}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			stage := attempt(t, c.step)
			r, err := Prompt(stage, c.inputs, c.baseline, roots())
			if err != nil {
				t.Fatal(err)
			}
			if r.PromptPath != filepath.Join(stage.Dir, PromptFile) || r.SkillsPath != filepath.Join(stage.Dir, SkillsDir) {
				t.Fatalf("paths = %q, %q", r.PromptPath, r.SkillsPath)
			}
			got, err := os.ReadFile(r.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(got, []byte("\r")) {
				t.Error("prompt contains a carriage return")
			}
			golden(t, c.kind+".md", got)
			sum := sha256.Sum256(got)
			if want := (File{Path: PromptFile, Digest: "sha256:" + hex.EncodeToString(sum[:])}); r.Manifest.Prompt != want {
				t.Errorf("manifest prompt = %+v, want %+v", r.Manifest.Prompt, want)
			}
			manifest, err := json.MarshalIndent(r.Manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden(t, c.kind+".manifest.json", append(manifest, '\n'))
		})
	}
}

// writeVariant copies a baseline source with every line ending replaced, as a CRLF checkout on Windows would hold it.
func writeVariant(t *testing.T, dir string, src Source, eol string) Source {
	t.Helper()
	data, err := os.ReadFile(src.Path)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.FromSlash(src.Name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.ReplaceAll(data, []byte("\n"), []byte(eol)), 0o644); err != nil {
		t.Fatal(err)
	}
	return Source{Name: src.Name, Path: path}
}

func TestManifestDigestsStableAcrossLineEndings(t *testing.T) {
	s := step(t, "review", "review", `{"actor":"artisan", "skills": ["go-tdd"]}`)
	lf := Baseline{Role: source("roles/reviewer.md"), Guide: source("stages/automated-review.md"),
		Standards: []Source{source("standards/review.md")}}
	in := []Input{input(t, "spec", "tasks/t7/spec.md", "spec.md")}
	want, err := Prompt(attempt(t, s), in, lf, roots())
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt, _ := os.ReadFile(want.PromptPath)
	for name, eol := range map[string]string{"crlf": "\r\n", "cr": "\r"} {
		dir := t.TempDir()
		b := Baseline{Role: writeVariant(t, dir, lf.Role, eol), Guide: writeVariant(t, dir, lf.Guide, eol),
			Standards: []Source{writeVariant(t, dir, lf.Standards[0], eol)}}
		got, err := Prompt(attempt(t, s), in, b, roots())
		if err != nil {
			t.Fatal(err)
		}
		gotPrompt, _ := os.ReadFile(got.PromptPath)
		if !bytes.Equal(gotPrompt, wantPrompt) {
			t.Errorf("%s: prompt differs from the LF render:\n%q", name, gotPrompt)
		}
		if g, w := mustJSON(t, got.Manifest), mustJSON(t, want.Manifest); g != w {
			t.Errorf("%s: manifest differs:\n%s\nwant\n%s", name, g, w)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestManifestPathsAreSlashSeparated(t *testing.T) {
	b := baseline("build")
	b.Standards = []Source{{Name: `standards\review.md`, Path: source("standards/review.md").Path}}
	r, err := Prompt(attempt(t, step(t, "build", "build", `{"skills": ["go-tdd"]}`)), nil, b, roots())
	if err != nil {
		t.Fatal(err)
	}
	m := r.Manifest
	paths := []string{m.Prompt.Path, m.Role.Path, m.Guide.Path, m.Standards[0].Path, m.Skills[0].Path}
	for _, f := range m.Skills[0].Files {
		paths = append(paths, f.Path)
	}
	want := []string{"prompt.md", "roles/artisan.md", "stages/build.md", "standards/review.md", "skills/go-tdd", "SKILL.md", "scripts/check.sh"}
	if !slices.Equal(paths, want) {
		t.Fatalf("manifest paths = %q, want %q", paths, want)
	}
}

func TestBadSourceNamesRefused(t *testing.T) {
	for _, name := range []string{"", "../outside.md", "/etc/role.md", "C:/role.md", "a/./b.md"} {
		b := baseline("build")
		b.Role.Name = name
		stage := attempt(t, step(t, "build", "build", ""))
		if _, err := Prompt(stage, nil, b, roots()); err == nil {
			t.Errorf("source name %q accepted", name)
		}
		assertAbsentOrEmpty(t, stage.Dir)
	}
}

func TestUnknownSkillRefused(t *testing.T) {
	stage := attempt(t, step(t, "build", "build", `{"skills": ["go-tdd", "no-such-skill"]}`))
	_, err := Prompt(stage, nil, baseline("build"), roots())
	if !errors.Is(err, ErrUnknownSkill) || !strings.Contains(err.Error(), `"no-such-skill"`) {
		t.Fatalf("err = %v", err)
	}
	assertAbsentOrEmpty(t, stage.Dir)
}

func assertAbsentOrEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) > 0 {
		t.Errorf("failed render left %d entries in %s", len(entries), dir)
	}
}

func TestSkillWithScriptCopiedByteIdenticalAndDigested(t *testing.T) {
	stage := attempt(t, step(t, "build", "build", `{"skills": ["go-tdd", "guildhall-contributing"]}`))
	r, err := Prompt(stage, nil, baseline("build"), roots())
	if err != nil {
		t.Fatal(err)
	}
	srcDir := filepath.Join("testdata", "skills", "release", "go-tdd")
	var lines strings.Builder
	for _, rel := range []string{"SKILL.md", "scripts/check.sh"} {
		src, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.ReadFile(filepath.Join(r.SkillsPath, "go-tdd", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(src, dst) {
			t.Errorf("%s not copied byte for byte", rel)
		}
		sum := sha256.Sum256(src)
		fmt.Fprintf(&lines, "%x  %s\n", sum, rel)
	}
	tree := sha256.Sum256([]byte(lines.String()))
	skill := r.Manifest.Skills[0]
	if skill.Name != "go-tdd" || skill.Root != "release" || skill.Path != "skills/go-tdd" {
		t.Errorf("skill = %+v", skill)
	}
	if want := "sha256:" + hex.EncodeToString(tree[:]); skill.Digest != want {
		t.Errorf("tree digest = %s, want %s", skill.Digest, want)
	}
	if got := r.Manifest.Skills[1]; got.Name != "guildhall-contributing" || got.Root != "managed" {
		t.Errorf("later root not used: %+v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(r.SkillsPath, "go-tdd", "scripts", "check.sh"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("script lost its executable bits: %v", info.Mode())
		}
	}
}

func TestSymlinkInSkillRefused(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "linked")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "secret"), filepath.Join(skill, "SKILL.md")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	stage := attempt(t, step(t, "build", "build", `{"skills": ["linked"]}`))
	_, err := Prompt(stage, nil, baseline("build"), []SkillRoot{{Name: "director", Dir: root}})
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v", err)
	}
	assertAbsentOrEmpty(t, stage.Dir)
}

func TestInputCopiesResolveFromPrompt(t *testing.T) {
	for _, name := range []string{"tasks/t7/spec.md", `tasks\t7\spec.md`} {
		t.Run(name, func(t *testing.T) {
			data := []byte("specification\r\nexact bytes\r\x00\n")
			src := filepath.Join(t.TempDir(), "spec.md")
			if err := os.WriteFile(src, data, 0o644); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			in := Input{Port: "spec", Name: name, Path: src, SHA256: hex.EncodeToString(sum[:])}
			otherPort := in
			otherPort.Port = "context"
			r, err := Prompt(attempt(t, step(t, "build", "build", "")), []Input{in, otherPort}, baseline("build"), roots())
			if err != nil {
				t.Fatal(err)
			}
			prompt, err := os.ReadFile(r.PromptPath)
			if err != nil {
				t.Fatal(err)
			}
			var rel, recordedDigest string
			for _, line := range strings.Split(string(prompt), "\n") {
				if strings.HasPrefix(line, "- spec: ") {
					if _, err := fmt.Sscanf(line, "- spec: %s %s", &rel, &recordedDigest); err != nil {
						t.Fatal(err)
					}
				}
			}
			if rel != "inputs/tasks/t7/spec.md" || recordedDigest != "(sha256:"+in.SHA256+")" {
				t.Fatalf("emitted input = %q %q", rel, recordedDigest)
			}
			copied, err := os.ReadFile(filepath.Join(filepath.Dir(r.PromptPath), filepath.FromSlash(rel)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(copied, data) {
				t.Fatalf("copy changed bytes: %q", copied)
			}
			for _, f := range r.Manifest.Inputs {
				if f.Path != "tasks/t7/spec.md" || f.Digest != "sha256:"+in.SHA256 {
					t.Fatalf("manifest input = %+v", f)
				}
			}
		})
	}
}

func TestInputCopyFailureCleansAttempt(t *testing.T) {
	for _, existingDir := range []bool{false, true} {
		for _, conflictingName := range []string{"tasks/t7/spec.md", "tasks/t7/spec.md/child"} {
			stage := attempt(t, step(t, "build", "build", ""))
			if existingDir {
				if err := os.MkdirAll(stage.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			inputs := []Input{
				input(t, "spec", "tasks/t7/spec.md", "spec.md"),
				input(t, "context", conflictingName, "notes.md"),
			}
			if _, err := Prompt(stage, inputs, baseline("build"), roots()); err == nil {
				t.Fatal("conflicting input copies accepted")
			}
			assertAbsentOrEmpty(t, stage.Dir)
		}
	}
}

func TestInputDigestMismatchRefused(t *testing.T) {
	in := input(t, "spec", "tasks/t7/spec.md", "spec.md")
	in.Path = filepath.Join("testdata", "docs", "notes.md")
	stage := attempt(t, step(t, "build", "build", ""))
	_, err := Prompt(stage, []Input{in}, baseline("build"), roots())
	if !errors.Is(err, ErrDigestMismatch) || !strings.Contains(err.Error(), "tasks/t7/spec.md") {
		t.Fatalf("err = %v", err)
	}
	assertAbsentOrEmpty(t, stage.Dir)
}

func TestRenderedAttemptNeverReplaced(t *testing.T) {
	stage := attempt(t, step(t, "build", "build", ""))
	if _, err := Prompt(stage, nil, baseline("build"), roots()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(stage.Dir, PromptFile))
	other := baseline("plan")
	if _, err := Prompt(stage, nil, other, roots()); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("second render err = %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(stage.Dir, PromptFile))
	if !bytes.Equal(before, after) {
		t.Fatal("rendered prompt was replaced")
	}
}

func TestPromptRefusesGuildmasterStage(t *testing.T) {
	stage := attempt(t, step(t, "accept", "guildmaster-approval", ""))
	if _, err := Prompt(stage, nil, baseline("build"), roots()); !errors.Is(err, ErrWrongActor) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuestionGoldenPerKind(t *testing.T) {
	spec := input(t, "spec", "tasks/t7/spec.md", "spec.md")
	cases := []struct {
		kind    string
		step    workflow.Step
		inputs  []Input
		options []string
	}{
		{"guildmaster-approval", step(t, "accept", "guildmaster-approval", `{"instructions": "Check the scope.\r\nThen decide."}`),
			[]Input{spec}, []string{"approved", "changes_requested"}},
		{"human-review", step(t, "review", "review", `{"actor":"guildmaster", "profile": "reviewer-opus"}`),
			[]Input{spec, {Port: "head", Name: "tasks/t7/head", SHA256: strings.Repeat("ab", 32)}},
			[]string{"approved", "changes_requested", "rejected"}},
		{"handoff-wait", step(t, "handoff", "handoff-wait", `{"from": "design team"}`), nil, []string{"received"}},
	}
	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			ask, err := Question(attempt(t, c.step), c.inputs)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(ask.Options, c.options) {
				t.Errorf("options = %q, want %q", ask.Options, c.options)
			}
			if strings.Contains(ask.Question, "\r") {
				t.Error("question contains a carriage return")
			}
			golden(t, "question-"+c.kind+".txt", []byte(ask.Question))
		})
	}
}

func TestQuestionNormalisesHandoffFrom(t *testing.T) {
	stage := attempt(t, step(t, "handoff", "handoff-wait", `{"from":"design team\r\nrelease team"}`))
	ask, err := Question(stage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ask.Question, "\r") || !strings.Contains(ask.Question, "from design team\nrelease team for") {
		t.Fatalf("question = %q", ask.Question)
	}
	if !slices.Equal(ask.Options, []string{"received"}) {
		t.Fatalf("options = %q", ask.Options)
	}
}

func TestQuestionRefusesArtisanStage(t *testing.T) {
	if _, err := Question(attempt(t, step(t, "build", "build", "")), nil); !errors.Is(err, ErrWrongActor) {
		t.Fatalf("err = %v", err)
	}
}
