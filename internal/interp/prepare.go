package interp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/strictjson"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// An attempt's preparation is rendered into partialDir and published by renaming it to promptDir,
// with its manifest inside as manifestFile (docs/interpreter.md#attempt-preparation).
const (
	promptDir    = "prompt"
	partialDir   = "prompt.partial"
	manifestFile = "manifest.json"
)

// prepared is an attempt's published preparation: what its every launch is given.
type prepared struct {
	promptFile string
	manifest   render.Manifest
}

// preparation returns attempt n's published preparation, checked against its manifest and inputs, or
// renders and publishes it when none was published. A published one is never rendered again or removed.
func (r *run) preparation(s workflow.Step, n int, inputs []render.Input) (prepared, error) {
	dir := r.attemptDir(s.ID, n)
	published := filepath.Join(dir, promptDir)
	switch _, err := os.Lstat(published); {
	case err == nil:
		return r.checkPrepared(s, n, published, inputs)
	case !errors.Is(err, fs.ErrNotExist):
		return prepared{}, err
	}
	// No launch was ever given a path inside an unpublished staging directory, so a leftover one goes.
	partial := filepath.Join(dir, partialDir)
	if err := os.RemoveAll(partial); err != nil {
		return prepared{}, err
	}
	b, err := r.binding(s.ID, n)
	if err != nil {
		return prepared{}, err
	}
	rendered, err := r.e.Prompter.Prompt(render.Stage{Repository: r.task.Repository, Task: r.task.ID, Step: s, Attempt: n, Dir: partial, Revisions: b.Revisions}, inputs)
	if err != nil {
		return prepared{}, err
	}
	data, err := json.Marshal(rendered.Manifest)
	if err != nil {
		return prepared{}, err
	}
	if err := writeFile(filepath.Join(partial, manifestFile), data); err != nil {
		return prepared{}, err
	}
	if err := os.Rename(partial, published); err != nil {
		return prepared{}, err
	}
	return prepared{promptFile: filepath.Join(published, render.PromptFile), manifest: rendered.Manifest}, nil
}

// checkPrepared reads a published preparation and checks it is attempt n's, given exactly inputs, and
// that the directory holds exactly the files its manifest names with the digests it names.
func (r *run) checkPrepared(s workflow.Step, n int, dir string, inputs []render.Input) (prepared, error) {
	refuse := func(format string, args ...any) (prepared, error) {
		return prepared{}, fmt.Errorf("%w: attempt %d of %s: %s", ErrPreparation, n, s.ID, fmt.Sprintf(format, args...))
	}
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if err != nil {
		return refuse("%v", err)
	}
	var m render.Manifest
	if err := strictjson.Decode(data, &m, strictjson.Options{Schema: render.ManifestSchemaPrefix, Version: render.ManifestSchemaVersion}); err != nil {
		// Wrapped, so a newer manifest's strictjson.ErrNewerSchema still tells a too-old reader from a damaged preparation.
		return prepared{}, fmt.Errorf("%w: attempt %d of %s: %s: %w", ErrPreparation, n, s.ID, manifestFile, err)
	}
	id := m.Identity
	if id.Repository != r.task.Repository || id.Task != r.task.ID || id.Step != s.ID || id.Attempt != n || id.Kind != s.Type || id.KindVersion != s.Version {
		return refuse("manifest names %s/%s step %s (%s version %d) attempt %d", id.Repository, id.Task, id.Step, id.Kind, id.KindVersion, id.Attempt)
	}
	want := make([]render.InputFile, len(inputs))
	for i, in := range inputs {
		d, err := digest.ParseHex(in.SHA256)
		if err != nil {
			return refuse("input %s: %v", in.Name, err)
		}
		want[i] = render.InputFile{Port: in.Port, Path: in.Name, Digest: string(d)}
	}
	if !slices.Equal(m.Inputs, want) {
		return refuse("manifest inputs %+v are not the recorded binding %+v", m.Inputs, want)
	}

	files := map[string]string{manifestFile: ""}
	expect := func(name, sum string) error {
		if old, ok := files[name]; ok && old != sum {
			return fmt.Errorf("manifest names %s twice with different digests", name)
		}
		files[name] = sum
		return nil
	}
	if m.Prompt.Path != render.PromptFile {
		return refuse("manifest prompt %q, want %q", m.Prompt.Path, render.PromptFile)
	}
	errs := []error{expect(render.PromptFile, m.Prompt.Digest)}
	for _, in := range m.Inputs {
		errs = append(errs, expect(path.Join(render.InputsDir, in.Path), in.Digest))
	}
	for _, sk := range m.Skills {
		if sk.Path != path.Join(render.SkillsDir, sk.Name) {
			return refuse("skill %q at %q", sk.Name, sk.Path)
		}
		if got := string(render.TreeDigest(sk.Files)); got != sk.Digest {
			return refuse("skill %q tree digest %s, its files digest to %s", sk.Name, sk.Digest, got)
		}
		for _, f := range sk.Files {
			errs = append(errs, expect(path.Join(sk.Path, f.Path), f.Digest))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return refuse("%v", err)
	}

	seen := map[string]bool{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		want, ok := files[name]
		switch {
		case !ok:
			return fmt.Errorf("%s is not in the manifest", name)
		case !d.Type().IsRegular():
			return fmt.Errorf("%s is not a regular file", name)
		}
		seen[name] = true
		if name == manifestFile {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if got := string(digest.Of(data)); got != want {
			return fmt.Errorf("%s is %s, manifest names %s", name, got, want)
		}
		return nil
	})
	if err != nil {
		return refuse("%v", err)
	}
	for name := range files {
		if !seen[name] {
			return refuse("%s is missing", name)
		}
	}
	return prepared{promptFile: filepath.Join(dir, render.PromptFile), manifest: m}, nil
}
