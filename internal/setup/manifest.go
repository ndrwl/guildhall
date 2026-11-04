package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// ManifestSchema is the format of the ownership manifest, manifest.json in the setup directory.
const ManifestSchema = "guildhall.setup-manifest/1"

// manifestFile is the manifest's name in the setup directory.
const manifestFile = "manifest.json"

// Owned is one file the engine created.
type Owned struct {
	Path    string    `json:"path"`
	Steps   string    `json:"steps"`
	Digest  string    `json:"digest"`
	Step    string    `json:"step"`
	Created time.Time `json:"created"`
}

// Manifest lists the files the engine created; the engine overwrites no other existing file.
type Manifest struct {
	Schema string  `json:"schema"`
	Files  []Owned `json:"files"`
	dir    string
}

// ReadManifest reads the ownership manifest of setupDir; a missing one is empty.
func ReadManifest(setupDir string) (Manifest, error) { return loadManifest(setupDir) }

func loadManifest(dir string) (Manifest, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return Manifest{}, fmt.Errorf("setup directory %q is not an absolute path", dir)
	}
	m := Manifest{Schema: ManifestSchema, dir: dir}
	data, err := os.ReadFile(filepath.Join(dir, manifestFile))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Schema != ManifestSchema {
		return Manifest{}, fmt.Errorf("%s: not a %s manifest", filepath.Join(dir, manifestFile), ManifestSchema)
	}
	m.dir = dir
	return m, nil
}

// Owns reports whether the manifest lists path.
func (m Manifest) Owns(path string) bool {
	return slices.ContainsFunc(m.Files, func(o Owned) bool { return o.Path == path })
}

// claim resolves a step's creates paths and refuses, with ErrNotOwned, any that exists and is not listed.
func (m Manifest) claim(e *env, st Step) ([]string, error) {
	var paths []string
	for _, c := range st.Creates {
		p := filepath.Clean(e.expand(c))
		if !filepath.IsAbs(p) {
			return nil, fmt.Errorf("creates %q is not an absolute path", p)
		}
		if _, err := os.Lstat(p); err == nil && !m.Owns(p) {
			return nil, fmt.Errorf("%w: %s exists and the manifest in %s does not list it; move it aside or remove it, then re-run", ErrNotOwned, p, m.dir)
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// record adds the paths of st that now exist to the manifest and writes it.
func (m Manifest) record(e *env, st Step, paths []string) error {
	changed := false
	for _, p := range paths {
		if _, err := os.Lstat(p); err != nil || m.Owns(p) {
			continue
		}
		m.Files = append(m.Files, Owned{Path: p, Steps: e.s.Name, Digest: e.s.Digest, Step: st.ID, Created: e.f.Now().UTC()})
		changed = true
	}
	if !changed {
		return nil
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Manifest{Schema: ManifestSchema, Files: m.Files}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(m.dir, manifestFile), append(data, '\n'))
}
