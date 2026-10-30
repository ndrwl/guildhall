package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// configOnlyCopy is a Director copy holding only its config file, and an installed release baseline.
func configOnlyCopy(t *testing.T) (cfg *config.Config, cfgDir, release string) {
	t.Helper()
	cfgDir = t.TempDir()
	data, err := os.ReadFile(filepath.Join("testdata", "run", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg, err = config.Load(filepath.Join(cfgDir, "config.toml")); err != nil {
		t.Fatal(err)
	}
	release = filepath.Join(t.TempDir(), "baseline")
	if err := baseline.Install(release); err != nil {
		t.Fatal(err)
	}
	return cfg, cfgDir, release
}

func renderStep(t *testing.T, p func(render.Stage, []render.Input) (render.Rendered, error), s workflow.Step) (render.Rendered, error) {
	t.Helper()
	return p(render.Stage{Repository: "app", Task: "t-1", Attempt: 1, Dir: filepath.Join(t.TempDir(), "attempt"), Step: s}, nil)
}

func TestBareCopyRendersEveryDefaultArtisanStage(t *testing.T) {
	cfg, cfgDir, release := configOnlyCopy(t)
	def, err := workflow.Default()
	if err != nil {
		t.Fatal(err)
	}
	p := testPrompter(cfg, cfgDir, release, t.TempDir())
	n := 0
	for _, s := range def.Steps {
		if s.Actor() != workflow.ArtisanActor {
			continue
		}
		n++
		r, err := renderStep(t, p, s)
		if err != nil {
			t.Errorf("%s: %v", s.ID, err)
			continue
		}
		m := r.Manifest
		if m.Role.Path != "roles/artisan.md" || m.Role.Root != "release" || m.Guide.Path != "stages/"+s.Type+".md" || m.Guide.Root != "release" || len(m.Standards) != 0 {
			t.Errorf("%s: role %+v, guide %+v, standards %+v", s.ID, m.Role, m.Guide, m.Standards)
		}
	}
	if n == 0 {
		t.Fatal("default workflow has no Artisan stage")
	}
}

func TestCopyBaselineWinsOverRelease(t *testing.T) {
	cfg, cfgDir, release := configOnlyCopy(t)
	for name, text := range map[string]string{"roles/artisan.md": "Copy role.\n", "standards/go.md": "Go.\n"} {
		path := filepath.Join(cfgDir, "baseline", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := renderStep(t, testPrompter(cfg, cfgDir, release, t.TempDir()), workflow.Step{ID: "build", Type: "build", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	m := r.Manifest
	if m.Role.Root != "copy" || m.Guide.Root != "release" || len(m.Standards) != 1 || m.Standards[0].Root != "copy" {
		t.Errorf("role %+v, guide %+v, standards %+v", m.Role, m.Guide, m.Standards)
	}
}

func TestKindWithoutAGuideIsRefused(t *testing.T) {
	cfg, cfgDir, release := configOnlyCopy(t)
	if err := os.Remove(filepath.Join(release, "stages", "build.md")); err != nil {
		t.Fatal(err)
	}
	_, err := renderStep(t, testPrompter(cfg, cfgDir, release, t.TempDir()), workflow.Step{ID: "build", Type: "build", Version: 1})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("render without a guide: %v, want not exist", err)
	}
}

func TestUnreadableCopyFileIsRefusedNotReplaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports a file used as a path component as ERROR_PATH_NOT_FOUND, which is fs.ErrNotExist")
	}
	cfg, cfgDir, release := configOnlyCopy(t)
	if err := os.MkdirAll(filepath.Join(cfgDir, "baseline"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "baseline", "roles"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := renderStep(t, testPrompter(cfg, cfgDir, release, t.TempDir()), workflow.Step{ID: "build", Type: "build", Version: 1})
	if err == nil {
		t.Errorf("render over an unreadable copy role: role %+v, want refused", r.Manifest.Role)
	}
}
