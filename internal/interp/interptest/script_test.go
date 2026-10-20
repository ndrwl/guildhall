package interptest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/render"
)

var update = flag.Bool("update", false, "rewrite golden fixtures")

// recorder is a TB that keeps what a Script reports, so failure modes can be asserted.
type recorder struct {
	mu       sync.Mutex
	errs     []string
	cleanups []func()
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

// end runs the cleanups as the end of a test would and returns everything reported.
func (r *recorder) end() []string {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	r.cleanups = nil
	return r.errs
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join(Testdata(), filepath.FromSlash(name))
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

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestScriptPassesLauncherContract(t *testing.T) {
	RunLauncherContract(t, func(t *testing.T) Harness {
		s := NewScript(t)
		responses := map[Outcome]Response{Succeeds: Succeed("built"), Fails: Fail("tests fail"), Hangs: Hang()}
		return Harness{Launcher: s, Adapter: interp.MockAdapter, Prepare: func(t *testing.T, o Outcome, render func(string) interp.Spec) interp.Spec {
			spec := render("")
			s.Expect(Expect(spec, responses[o]))
			return spec
		}}
	})
}

func TestFixtureManifests(t *testing.T) {
	for name, a := range map[string]Attempt{"build": Build(t, "spec-v1.md"), "automated-review": AutomatedReview(t, "spec-v1.md")} {
		t.Run(name, func(t *testing.T) {
			data, err := json.MarshalIndent(a.Render(t).Manifest, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden(t, "manifests/"+name+".json", append(data, '\n'))
		})
	}
}

func TestMatchedLaunchResponds(t *testing.T) {
	cases := []struct {
		name    string
		attempt func(t *testing.T) Attempt
		respond func(t *testing.T) Response
		// outputs maps each output file to its golden fixture.
		outputs map[string]string
	}{
		{"succeeded", func(t *testing.T) Attempt { return Build(t, "spec-v1.md") },
			func(t *testing.T) Response {
				return Succeed("Built the sorted task list.", File{Path: "notes/build.md", Data: []byte("Sorted by id.\n")})
			}, nil},
		{"failed", func(t *testing.T) Attempt { return Build(t, "spec-v1.md") },
			func(t *testing.T) Response { return Fail("Tests fail on Windows.") }, nil},
		{"review", func(t *testing.T) Attempt { return AutomatedReview(t, "spec-v1.md") },
			func(t *testing.T) Response { return Review(ReviewFixture(t, "changes-requested")) },
			map[string]string{ReviewOutcomeFile: "reviews/changes-requested.json"}},
		{"blocked", func(t *testing.T) Attempt { return Build(t, "spec-v1.md") },
			func(t *testing.T) Response {
				return Blocked("Should the list be sorted by id or by title?", "id", "title")
			}, map[string]string{interp.BlockedQuestionFile: "results/blocked-question.json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec := c.attempt(t).Render(t)
			s := NewScript(t, Expect(spec, c.respond(t)))
			h, err := s.Launch(context.Background(), spec)
			if err != nil {
				t.Fatal(err)
			}
			if h.Identity != spec.Identity || h.Ref == "" {
				t.Errorf("handle = %+v", h)
			}
			golden(t, "results/"+c.name+".json", readFile(t, spec.ResultFile))
			if _, err := adapter.ReadResult(spec.ResultFile, spec.Identity); err != nil {
				t.Errorf("result does not validate: %v", err)
			}
			for out, fixture := range c.outputs {
				golden(t, fixture, readFile(t, filepath.Join(spec.WorkDir, out)))
			}
			if got := s.Launched(); len(got) != 1 || got[0].PromptFile != spec.PromptFile {
				t.Errorf("Launched = %+v", got)
			}
		})
	}
}

func TestScriptFailsTheTest(t *testing.T) {
	build := func(t *testing.T) interp.Spec { return Build(t, "spec-v1.md").Render(t) }
	cases := []struct {
		name string
		// arrange returns the spec to launch and the script's expectations.
		arrange func(t *testing.T) (interp.Spec, []Expectation)
		want    string
	}{
		{"real adapter refused by the guard", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			spec.Adapter = "claude"
			return spec, []Expectation{e}
		}, "adapter refused"},
		{"fake adapter is not the mock", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			spec.Adapter = interp.FakeAdapter
			return spec, []Expectation{e}
		}, `want "mock"`},
		{"script exhausted", func(t *testing.T) (interp.Spec, []Expectation) { return build(t), nil }, "script exhausted"},
		{"another step", func(t *testing.T) (interp.Spec, []Expectation) {
			return build(t), []Expectation{Expect(AutomatedReview(t, "spec-v1.md").Render(t), Succeed("reviewed"))}
		}, `unexpected launch: step "build", want step "review"`},
		{"another attempt", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Attempt = 2
			return spec, []Expectation{e}
		}, "unexpected launch: attempt 1, want 2"},
		{"another profile", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Profile = "builder-opus"
			return spec, []Expectation{e}
		}, `profile "builder", want "builder-opus"`},
		{"wrong artefact revision", func(t *testing.T) (interp.Spec, []Expectation) {
			e := Expect(build(t), Succeed("built"))
			return Build(t, "spec-v2.md").Render(t), []Expectation{e}
		}, `wrong artefact revision on input "spec"`},
		{"missing input", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Inputs["context"] = FixtureDigest(t, "docs/spec-v1.md")
			return spec, []Expectation{e}
		}, `manifest lacks required input "context"`},
		{"unexpected input", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Inputs = nil
			return spec, []Expectation{e}
		}, `unexpected input "spec"`},
		{"missing standard", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Standards = append(e.Standards, "standards/review.md")
			return spec, []Expectation{e}
		}, `manifest lacks required standard "standards/review.md"`},
		{"missing skill", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Skills = append(e.Skills, "go-review")
			return spec, []Expectation{e}
		}, `manifest lacks required skill "go-review"`},
		{"skills out of order", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Skills = []string{"guildhall-contributing", "go-tdd"}
			return spec, []Expectation{e}
		}, "skills in order"},
		{"unexpected skill", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			e.Skills = e.Skills[:1]
			return spec, []Expectation{e}
		}, `unexpected skill "guildhall-contributing"`},
		{"manifest without role", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			e := Expect(spec, Succeed("built"))
			spec.Manifest.Role = render.File{}
			return spec, []Expectation{e}
		}, "manifest lacks the role"},
		{"prompt changed after rendering", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			appendTo(t, spec.PromptFile)
			return spec, []Expectation{Expect(spec, Succeed("built"))}
		}, "prompt file does not match the manifest"},
		{"input copy changed", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			appendTo(t, filepath.Join(filepath.Dir(spec.PromptFile), render.InputsDir, "tasks", "t7", "spec-v1.md"))
			return spec, []Expectation{Expect(spec, Succeed("built"))}
		}, "input spec does not match the manifest"},
		{"skill script changed", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			appendTo(t, filepath.Join(filepath.Dir(spec.PromptFile), render.SkillsDir, "go-tdd", "scripts", "check.sh"))
			return spec, []Expectation{Expect(spec, Succeed("built"))}
		}, "skill go-tdd file scripts/check.sh does not match the manifest"},
		{"skill without files", func(t *testing.T) (interp.Spec, []Expectation) {
			spec := build(t)
			spec.Manifest.Skills[0].Files = nil
			return spec, []Expectation{Expect(spec, Succeed("built"))}
		}, `manifest lists no files for skill "go-tdd"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spec, exps := c.arrange(t)
			rec := &recorder{}
			s := NewScript(rec, exps...)
			if _, err := s.Launch(context.Background(), spec); !errors.Is(err, ErrScript) || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Launch = %v, want ErrScript naming %q", err, c.want)
			}
			if errs := rec.end(); len(errs) != 1 || !strings.Contains(errs[0], c.want) {
				t.Errorf("reported %q, want one failure naming %q", errs, c.want)
			}
			if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a refused launch wrote a result: %v", err)
			}
		})
	}
}

func appendTo(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("changed\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUnlaunchedExpectationsFailTheTest(t *testing.T) {
	rec := &recorder{}
	spec := Build(t, "spec-v1.md").Render(t)
	review := AutomatedReview(t, "spec-v1.md").Render(t)
	s := NewScript(rec, Expect(spec, Succeed("built")), Expect(review, Review(ReviewFixture(t, "approved"))))
	if _, err := s.Launch(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if errs := rec.end(); len(errs) != 1 || !strings.Contains(errs[0], "1 expected launches never happened: review") {
		t.Errorf("reported %q, want the unlaunched review", errs)
	}
}

func TestExistingResultIsNeverReplaced(t *testing.T) {
	spec := Build(t, "spec-v1.md").Render(t)
	if err := adapter.WriteResult(spec.ResultFile, adapter.Result{Identity: spec.Identity, Status: adapter.StatusFailed, Summary: "first"}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, spec.ResultFile)
	s := NewScript(t, Expect(spec, Succeed("second")))
	if _, err := s.Launch(context.Background(), spec); !errors.Is(err, adapter.ErrResultExists) {
		t.Errorf("Launch = %v, want ErrResultExists", err)
	}
	if after := readFile(t, spec.ResultFile); !bytes.Equal(before, after) {
		t.Errorf("result replaced: %s", after)
	}
}

func TestResponsePathsValidatedBeforeWriting(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "notes/../escape", "./output", "notes/./output", "notes//output", "notes/", `notes\output`, "/absolute", "result.json", "Result.json", ReviewOutcomeFile, strings.ToUpper(ReviewOutcomeFile), interp.BlockedQuestionFile, strings.ToUpper(interp.BlockedQuestionFile)} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", name, existing), func(t *testing.T) {
				spec := Build(t, "spec-v1.md").Render(t)
				resultName := "result.json"
				r := Succeed("built", File{Path: name, Data: []byte("replacement")})
				switch strings.ToLower(name) {
				case ReviewOutcomeFile:
					resultName = name
					r = Review(ReviewFixture(t, "approved"))
				case interp.BlockedQuestionFile:
					resultName = name
					r = Blocked("Which option?", "one", "two")
				}
				spec.ResultFile = filepath.Join(spec.WorkDir, resultName)
				var before []byte
				if existing {
					if err := adapter.WriteResult(spec.ResultFile, adapter.Result{Identity: spec.Identity, Status: adapter.StatusFailed, Summary: "original"}); err != nil {
						t.Fatal(err)
					}
					before = readFile(t, spec.ResultFile)
				}
				r.files = append([]File{{Path: "first.txt", Data: []byte("must not be written")}}, r.files...)
				s := NewScript(t, Expect(spec, r))
				if _, err := s.Launch(context.Background(), spec); err == nil {
					t.Fatal("Launch accepted an unsafe or colliding output")
				}
				if _, err := os.Stat(filepath.Join(spec.WorkDir, "first.txt")); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("response wrote a file before validating all paths: %v", err)
				}
				if existing {
					if after := readFile(t, spec.ResultFile); !bytes.Equal(before, after) {
						t.Errorf("result replaced: %s", after)
					}
				} else if _, err := os.Stat(spec.ResultFile); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("refused response wrote a result: %v", err)
				}
			})
		}
	}
}

func TestResponseRejectsExistingResultAlias(t *testing.T) {
	spec := Build(t, "spec-v1.md").Render(t)
	if err := adapter.WriteResult(spec.ResultFile, adapter.Result{Identity: spec.Identity, Status: adapter.StatusFailed, Summary: "original"}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, spec.ResultFile)
	alias := filepath.Join(spec.WorkDir, "alias.json")
	if err := os.Link(spec.ResultFile, alias); err != nil {
		t.Fatal(err)
	}
	s := NewScript(t, Expect(spec, Succeed("built",
		File{Path: "first.txt", Data: []byte("must not be written")},
		File{Path: "alias.json", Data: []byte("replacement")},
	)))
	if _, err := s.Launch(context.Background(), spec); err == nil {
		t.Fatal("Launch accepted an existing result alias")
	}
	if _, err := os.Stat(filepath.Join(spec.WorkDir, "first.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("response wrote a file before validating all paths: %v", err)
	}
	for _, path := range []string{spec.ResultFile, alias} {
		if after := readFile(t, path); !bytes.Equal(before, after) {
			t.Errorf("%s replaced: %s", path, after)
		}
	}
}

func TestConcurrentLaunches(t *testing.T) {
	const n = 8
	specs := make([]interp.Spec, n)
	s := NewScript(t)
	for i := range specs {
		specs[i] = Build(t, "spec-v1.md").Render(t)
		s.Expect(Expect(specs[i], Succeed("built")))
	}
	var wg sync.WaitGroup
	for _, spec := range specs {
		wg.Go(func() {
			h, err := s.Launch(context.Background(), spec)
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.Stop(h); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := len(s.Launched()); got != n {
		t.Errorf("Launched %d, want %d", got, n)
	}
}

func TestInvalidReviewPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Review accepted an invalid outcome")
		}
	}()
	o := ReviewFixture(t, "approved")
	o.Verdict = "maybe"
	Review(o)
}
