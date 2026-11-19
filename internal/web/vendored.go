package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/ndrwl/guildhall/internal/strictjson"
)

// vendorManifest is one vendored package's manifest.json under vendor/<package>/: the upstream name, version and
// licence, where its files came from, and each file's sha256 pin (docs/decisions/list-page-scripts.md). Every file
// of the package's directory but the manifest is listed, and the bytes Guildhall serves are the pinned ones.
type vendorManifest struct {
	Schema          string       `json:"schema"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	License         string       `json:"license"`
	LicenseFile     string       `json:"license_file"`
	Homepage        string       `json:"homepage"`
	Source          string       `json:"source"`
	SourceSHA1      string       `json:"source_sha1"`
	SourceIntegrity string       `json:"source_integrity"`
	Files           []vendorFile `json:"files"`
}

// vendorFile is one vendored file: its name in the package's directory, its path in the upstream source, its use
// ("script" inlined on the task list and listed in the CSP, "style" inlined in the list's stylesheet, or "license")
// and the sha256 of its upstream bytes, in hex.
type vendorFile struct {
	Path     string `json:"path"`
	Upstream string `json:"upstream"`
	Use      string `json:"use"`
	SHA256   string `json:"sha256"`
}

// vendorLicenses are the licences a vendored file may carry, as SPDX ids.
var vendorLicenses = []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0"}

// vendored is every vendored package's script and style, read and checked once when the package loads.
type vendored struct {
	Manifests []vendorManifest
	// Scripts are the vendored scripts, inlined on the task list before the own scripts and listed in the CSP.
	Scripts []ownScript
	// Style is the vendored stylesheets, joined, inlined on the task list after the pages' stylesheet.
	Style template.CSS
}

var vendor = mustLoadVendor(assets)

func mustLoadVendor(fsys fs.FS) vendored {
	v, err := loadVendor(fsys)
	if err != nil {
		panic("web: " + err.Error())
	}
	return v
}

// loadVendor reads every vendor/<package>/manifest.json of fsys in directory order and checks it: a schema it knows,
// an allowed licence whose file is listed, every listed file present with its pinned sha256, and no file of the
// directory left unlisted. A script holding </script is refused, as an own one is.
func loadVendor(fsys fs.FS) (vendored, error) {
	var out vendored
	names, err := fs.Glob(fsys, "vendor/*/manifest.json")
	if err != nil {
		return vendored{}, err
	}
	var style strings.Builder
	for _, name := range names {
		dir := path.Dir(name)
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return vendored{}, err
		}
		var m vendorManifest
		if err := strictjson.Decode(raw, &m, strictjson.Options{Schema: "guildhall.vendor-manifest/", Version: 1, MaxBytes: 64 << 10}); err != nil {
			return vendored{}, fmt.Errorf("%s: %w", name, err)
		}
		if err := checkManifest(m); err != nil {
			return vendored{}, fmt.Errorf("%s: %w", name, err)
		}
		listed := map[string]bool{"manifest.json": true}
		for _, f := range m.Files {
			listed[f.Path] = true
			b, err := fs.ReadFile(fsys, dir+"/"+f.Path)
			if err != nil {
				return vendored{}, fmt.Errorf("%s: %w", name, err)
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != f.SHA256 {
				return vendored{}, fmt.Errorf("%s: %s does not match its sha256 pin", name, f.Path)
			}
			switch f.Use {
			case "script":
				if strings.Contains(strings.ToLower(string(b)), "</script") {
					return vendored{}, fmt.Errorf("%s: %s holds </script", name, f.Path)
				}
				out.Scripts = append(out.Scripts, ownScript{Name: f.Path, Body: template.JS(b), //nolint:gosec // a pinned vendored file, never input
					Hash: "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"})
			case "style":
				if strings.Contains(strings.ToLower(string(b)), "</style") {
					return vendored{}, fmt.Errorf("%s: %s holds </style", name, f.Path)
				}
				style.Write(b)
				style.WriteByte('\n')
			}
		}
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			return vendored{}, err
		}
		for _, e := range entries {
			if !listed[e.Name()] {
				return vendored{}, fmt.Errorf("%s: %s is not listed", name, e.Name())
			}
		}
		out.Manifests = append(out.Manifests, m)
	}
	out.Style = template.CSS(style.String()) //nolint:gosec // pinned vendored files, never input
	return out, nil
}

// checkManifest refuses a manifest without its name, version, source or files, with a licence not allowed or not
// listed, or with a file that is not a plain name, is listed twice, has no known use or no sha256 pin.
func checkManifest(m vendorManifest) error {
	switch {
	case m.Name == "" || m.Version == "" || m.Source == "" || len(m.Files) == 0:
		return errors.New("a manifest names its package, version, source and files")
	case !slices.Contains(vendorLicenses, m.License):
		return fmt.Errorf("licence %q is not one of %s", m.License, strings.Join(vendorLicenses, ", "))
	}
	seen := map[string]bool{}
	licence := false
	for _, f := range m.Files {
		if f.Path == "" || f.Path == "manifest.json" || f.Path != path.Base(f.Path) || strings.HasPrefix(f.Path, ".") || seen[f.Path] {
			return fmt.Errorf("file %q is not a plain name listed once", f.Path)
		}
		seen[f.Path] = true
		if f.Use != "script" && f.Use != "style" && f.Use != "license" {
			return fmt.Errorf("file %s has use %q, want script, style or license", f.Path, f.Use)
		}
		if b, err := hex.DecodeString(f.SHA256); err != nil || len(b) != sha256.Size || f.SHA256 != strings.ToLower(f.SHA256) {
			return fmt.Errorf("file %s has no lowercase hex sha256 pin", f.Path)
		}
		licence = licence || f.Use == "license" && f.Path == m.LicenseFile
	}
	if !licence {
		return fmt.Errorf("licence file %q is not listed with use license", m.LicenseFile)
	}
	return nil
}
