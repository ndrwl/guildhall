package interp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/testenv"
)

// childEnv makes the test binary act as a worker that reads one copied input once released.
const childEnv = "GUILDHALL_INTERP_TEST_CHILD"

func TestMain(m *testing.M) {
	store.SkipSyncForTests()
	if os.Getenv(childEnv) == "1" {
		os.Exit(child(os.Args[len(os.Args)-3:]))
	}
	os.Exit(testenv.RunWithClaudeGuard(m))
}

// child waits for the release file, then copies the input it was given to out.
func child(args []string) int {
	input, release, out := args[0], args[1], args[2]
	for deadline := time.Now().Add(testenv.Ceiling(time.Minute)); !exists(release); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			return 2
		}
	}
	data, err := os.ReadFile(input)
	if err != nil {
		data = []byte("read failed: " + err.Error())
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return 1
	}
	return 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// buildLauncher hands every build launch to launch and every other launch to the script.
type buildLauncher struct {
	script *interptest.Script
	launch func(call int, spec interp.Spec) (interp.Handle, error)
	specs  []interp.Spec
}

func (l *buildLauncher) Launch(ctx context.Context, spec interp.Spec) (interp.Handle, error) {
	if spec.Manifest.Identity.Step != "build" {
		return l.script.Launch(ctx, spec)
	}
	l.specs = append(l.specs, spec)
	return l.launch(len(l.specs), spec)
}

func (l *buildLauncher) Stop(h interp.Handle) error { return nil }

var (
	errAckLost   = errors.New("launch accepted; acknowledgement lost")
	errLiveOwner = errors.New("attempt owner is still live; duplicate refused")
)

// mutableBaseline renders e's prompts from a copy of the fixture baseline and skills, so a test can
// change them between advances, and returns the copy's root.
func mutableBaseline(t *testing.T, e *env) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"baseline", "skills"} {
		if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(interptest.Testdata(), dir))); err != nil {
			t.Fatal(err)
		}
	}
	e.eng.Prompter = interp.PromptFunc(func(stage render.Stage, inputs []render.Input) (render.Rendered, error) {
		src := func(name string) render.Source {
			return render.Source{Name: name, Path: filepath.Join(root, "baseline", filepath.FromSlash(name))}
		}
		b := render.Baseline{Role: src("roles/artisan.md"), Guide: src("stages/" + stage.Step.Type + ".md"), Standards: []render.Source{src("standards/go.md")}}
		roots := []render.SkillRoot{
			{Name: "release", Dir: filepath.Join(root, "skills", "release")},
			{Name: "managed", Dir: filepath.Join(root, "skills", "managed")},
		}
		return render.Prompt(stage, inputs, b, roots)
	})
	return root
}

// toBuild advances task app/t7 through the plan and its approval, so the next advance launches the build.
// The approval's advance is the first build launch; its error is returned.
func toBuild(t *testing.T, e *env, l *buildLauncher) error {
	t.Helper()
	e.eng.Launcher = l
	e.advance(t, interp.Event{Kind: interp.TaskCreated})
	e.advance(t, interp.Event{Kind: interp.AttemptResult, Step: "plan", Attempt: 1})
	e.answer(t, "accept-1", "approved")
	return advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
}

func buildDir(e *env) string {
	return filepath.Join(e.eng.StateDir, "app", "t7", "build-1")
}

// snapshot maps every path under dir, slash-separated, to its bytes; a directory maps to "/".
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[filepath.ToSlash(rel)] = "/"
			return nil
		}
		data, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func sameSnapshot(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if maps.Equal(got, want) {
		return
	}
	for p := range want {
		if g, ok := got[p]; !ok {
			t.Errorf("%s: %s was removed", what, p)
		} else if g != want[p] {
			t.Errorf("%s: %s was rewritten", what, p)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("%s: %s was added", what, p)
		}
	}
}

// change rewrites the copied baseline's role and the build's go-tdd skill, adding a file to its tree.
func change(t *testing.T, root string) {
	t.Helper()
	for name, data := range map[string]string{
		"baseline/roles/artisan.md":                      "A role from later configuration.\n",
		"skills/release/go-tdd/SKILL.md":                 "A later go-tdd.\n",
		"skills/release/go-tdd/scripts/x":                "added later\n",
		"baseline/standards/go.md":                       "A later standard.\n",
		"skills/managed/guildhall-contributing/SKILL.md": "A later contributing guide.\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLostAcknowledgementKeepsPreparation(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, straightScript(t)[0])
			root := mutableBaseline(t, e)
			var before map[string]string
			l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
				if call == 1 {
					before = snapshot(t, filepath.Dir(spec.PromptFile))
					return interp.Handle{}, errAckLost
				}
				return interp.Handle{}, errLiveOwner
			}}
			if err := toBuild(t, e, l); !errors.Is(err, errAckLost) {
				t.Fatalf("first build launch: %v, want the lost acknowledgement", err)
			}
			change(t, root)
			if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, errLiveOwner) {
				t.Fatalf("replay: %v, want the live owner's refusal", err)
			}
			if len(l.specs) != 2 {
				t.Fatalf("build launched %d times, want 2", len(l.specs))
			}
			if !reflect.DeepEqual(l.specs[1], l.specs[0]) {
				t.Errorf("replay launched %+v, want the first spec %+v", l.specs[1], l.specs[0])
			}
			sameSnapshot(t, "prompt directory after the refused duplicate", snapshot(t, filepath.Dir(l.specs[0].PromptFile)), before)
			if exists(filepath.Join(buildDir(e), "launched.json")) {
				t.Error("a refused launch was marked launched")
			}
		})
	}
}

func TestLostLaunchedMarkReplaysTheSamePreparation(t *testing.T) {
	e := newEnv(t, backends[1], straightScript(t)[0])
	root := mutableBaseline(t, e)
	var before map[string]string
	l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
		if call == 1 {
			before = snapshot(t, filepath.Dir(spec.PromptFile))
		}
		return interp.Handle{Identity: spec.Identity, Ref: fmt.Sprintf("owner-%d", call)}, nil
	}}
	if err := toBuild(t, e, l); err != nil {
		t.Fatal(err)
	}
	// The engine stopped after Launch returned and before the mark.
	if err := os.Remove(filepath.Join(buildDir(e), "launched.json")); err != nil {
		t.Fatal(err)
	}
	change(t, root)
	e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
	if len(l.specs) != 2 || !reflect.DeepEqual(l.specs[1], l.specs[0]) {
		t.Fatalf("replay launched %+v, want the first spec again", l.specs)
	}
	sameSnapshot(t, "prompt directory after the replay", snapshot(t, filepath.Dir(l.specs[0].PromptFile)), before)
	if !exists(filepath.Join(buildDir(e), "launched.json")) {
		t.Error("the replayed launch was not marked")
	}
}

func TestPublishedPreparation(t *testing.T) {
	e := newEnv(t, backends[0], straightScript(t)[0])
	l := &buildLauncher{script: e.script, launch: func(int, interp.Spec) (interp.Handle, error) { return interp.Handle{Ref: "owner"}, nil }}
	// A crash mid-render left an unpublished staging directory, which no launch was given.
	partial := filepath.Join(buildDir(e), "prompt.partial")
	if err := os.MkdirAll(filepath.Join(partial, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "prompt.md"), []byte("half a prompt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := toBuild(t, e, l); err != nil {
		t.Fatal(err)
	}
	spec := l.specs[0]
	if want := filepath.Join(buildDir(e), "prompt", render.PromptFile); spec.PromptFile != want {
		t.Errorf("prompt file %s, want %s", spec.PromptFile, want)
	}
	if exists(partial) {
		t.Error("the staging directory is left after publishing")
	}
	data, err := os.ReadFile(filepath.Join(buildDir(e), "prompt", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m render.Manifest
	if err := json.Unmarshal(data, &m); err != nil || !reflect.DeepEqual(m, spec.Manifest) {
		t.Errorf("published manifest %+v (%v), want the launched one %+v", m, err, spec.Manifest)
	}
	got, err := os.ReadFile(spec.PromptFile)
	if err != nil || bytes.Contains(got, []byte("half a prompt")) {
		t.Errorf("published prompt: %v, %q", err, got)
	}
}

func TestInvalidPreparationIsRefused(t *testing.T) {
	editManifest := func(edit func(m *render.Manifest)) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			p := filepath.Join(dir, "manifest.json")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var m render.Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			edit(&m)
			if data, err = json.Marshal(m); err != nil {
				t.Fatal(err)
			}
			write(t, p, string(data))
		}
	}
	skill := filepath.Join("skills", "go-tdd")
	rewriteManifest := func(rewrite func(data string) string) func(t *testing.T, dir string) {
		return func(t *testing.T, dir string) {
			p := filepath.Join(dir, "manifest.json")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p, rewrite(string(data)))
		}
	}
	cases := []struct {
		name    string
		corrupt func(t *testing.T, dir string)
	}{
		{"manifest missing", func(t *testing.T, dir string) { remove(t, filepath.Join(dir, "manifest.json")) }},
		{"manifest truncated", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "manifest.json"), `{"schema":`) }},
		{"manifest unknown field", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "manifest.json")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p, strings.Replace(string(data), "{", `{"extra":1,`, 1))
		}},
		{"manifest trailing data", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "manifest.json")
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			write(t, p, string(data)+"{}")
		}},
		{"manifest schema", editManifest(func(m *render.Manifest) { m.Schema = "guildhall.prompt-manifest/x" })},
		{"manifest schema newer", editManifest(func(m *render.Manifest) { m.Schema = "guildhall.prompt-manifest/2" })},
		{"manifest duplicate inputs", rewriteManifest(func(data string) string {
			return strings.Replace(data, "{", `{"inputs":[],`, 1)
		})},
		{"manifest nested unknown field", rewriteManifest(func(data string) string {
			return strings.Replace(data, `"identity":{`, `"identity":{"extra":1,`, 1)
		})},
		{"manifest utf-16", rewriteManifest(func(data string) string {
			return "\xff\xfe" + strings.Join(strings.Split(data, ""), "\x00") + "\x00"
		})},
		{"manifest unpaired surrogate", rewriteManifest(func(data string) string {
			return strings.Replace(data, `"repository":"`, `"repository":"\ud800`, 1)
		})},
		{"another attempt", editManifest(func(m *render.Manifest) { m.Identity.Attempt = 2 })},
		{"another step", editManifest(func(m *render.Manifest) { m.Identity.Step = "review" })},
		{"inputs not the binding", editManifest(func(m *render.Manifest) { m.Inputs[0].Port = "notes" })},
		{"skill tree digest", editManifest(func(m *render.Manifest) { m.Skills[0].Digest = m.Skills[1].Digest })},
		{"prompt missing", func(t *testing.T, dir string) { remove(t, filepath.Join(dir, render.PromptFile)) }},
		{"prompt changed", func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, render.PromptFile), "a different prompt\n")
		}},
		{"input changed", func(t *testing.T, dir string) {
			entries, err := os.ReadDir(filepath.Join(dir, render.InputsDir))
			if err != nil || len(entries) == 0 {
				t.Fatalf("inputs: %v %v", entries, err)
			}
			err = filepath.WalkDir(filepath.Join(dir, render.InputsDir), func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					write(t, p, "changed input\n")
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"skill file changed", func(t *testing.T, dir string) { write(t, filepath.Join(dir, skill, "SKILL.md"), "changed\n") }},
		{"skill file removed", func(t *testing.T, dir string) { remove(t, filepath.Join(dir, skill, "SKILL.md")) }},
		{"skill file added", func(t *testing.T, dir string) { write(t, filepath.Join(dir, skill, "extra.md"), "extra\n") }},
		{"file added", func(t *testing.T, dir string) { write(t, filepath.Join(dir, "notes.txt"), "extra\n") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, backends[0], straightScript(t)[0])
			l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
				if call == 1 {
					return interp.Handle{}, errAckLost
				}
				return interp.Handle{Ref: "owner"}, nil
			}}
			if err := toBuild(t, e, l); !errors.Is(err, errAckLost) {
				t.Fatalf("first build launch: %v", err)
			}
			dir := filepath.Join(buildDir(e), "prompt")
			c.corrupt(t, dir)
			before := snapshot(t, buildDir(e))
			err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"})
			if !errors.Is(err, interp.ErrPreparation) {
				t.Errorf("replay over invalid preparation: %v, want ErrPreparation", err)
			}
			if newer := c.name == "manifest schema newer"; newer != errors.Is(err, strictjson.ErrNewerSchema) {
				t.Errorf("replay over invalid preparation: %v, wraps ErrNewerSchema %v, want %v", err, !newer, newer)
			}
			if len(l.specs) != 1 {
				t.Errorf("invalid preparation was launched: %d launches", len(l.specs))
			}
			sameSnapshot(t, "attempt directory after the refusal", snapshot(t, buildDir(e)), before)
		})
	}
}

// TestLiveWorkerReadsOriginalInput launches the test binary as the build's worker. Its acknowledgement
// is lost, the recorded document drifts, and the replay is refused as a live duplicate; the worker,
// released only then, must still read the input bytes it was prepared with.
func TestLiveWorkerReadsOriginalInput(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, backends[1], straightScript(t)[0])
	scratch := t.TempDir()
	release, out := filepath.Join(scratch, "release"), filepath.Join(scratch, "out")
	var cmd *exec.Cmd
	var original []byte
	l := &buildLauncher{script: e.script, launch: func(call int, spec interp.Spec) (interp.Handle, error) {
		if call > 1 {
			return interp.Handle{}, errLiveOwner
		}
		input := filepath.Join(filepath.Dir(spec.PromptFile), render.InputsDir, filepath.FromSlash(spec.Manifest.Inputs[0].Path))
		var err error
		if original, err = os.ReadFile(input); err != nil {
			return interp.Handle{}, err
		}
		cmd = exec.Command(exe, "-test.run=^$", input, release, out)
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if err := cmd.Start(); err != nil {
			return interp.Handle{}, err
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return interp.Handle{}, errAckLost
	}}
	if err := toBuild(t, e, l); !errors.Is(err, errAckLost) {
		t.Fatalf("first build launch: %v", err)
	}
	// The recorded document drifts after preparation; rendering it again would refuse or replace the copy.
	doc := filepath.Join(e.eng.DocumentDir, filepath.FromSlash(l.specs[0].Manifest.Inputs[0].Path))
	write(t, doc, "a drifted document\n")
	if err := advanceErr(t, e, interp.Event{Kind: interp.DecisionAnswered, Decision: "accept-1"}); !errors.Is(err, errLiveOwner) {
		t.Fatalf("replay: %v, want the live owner's refusal", err)
	}
	write(t, release, "")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("worker: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("worker read %q, want the original input %q", got, original)
	}
}

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, p string) {
	t.Helper()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
}
