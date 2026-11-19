package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestVendorManifests: every directory under vendor/ is one vendored package with a manifest, and the embedded uPlot
// is 1.6.32, MIT, with its licence file, each file's bytes matching its sha256 pin, the script's own banner naming
// the manifest's version, and THIRD_PARTY_NOTICES.md naming it with its licence's text
// (docs/decisions/list-page-scripts.md).
func TestVendorManifests(t *testing.T) {
	entries, err := fs.ReadDir(assets, "vendor")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(vendor.Manifests) {
		t.Fatalf("vendor/ holds %d entries for %d manifests", len(entries), len(vendor.Manifests))
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("vendor/%s is a file outside any vendored package", e.Name())
		}
	}
	if len(vendor.Manifests) != 1 {
		t.Fatalf("%d vendored packages, want uPlot alone", len(vendor.Manifests))
	}
	m := vendor.Manifests[0]
	if m.Name != "uPlot" || m.Version != "1.6.32" || m.License != "MIT" || m.LicenseFile != "LICENSE" ||
		m.Source != "https://registry.npmjs.org/uplot/-/uplot-1.6.32.tgz" {
		t.Errorf("manifest %+v, want uPlot 1.6.32, MIT, from the npm registry", m)
	}
	pins := map[string]string{
		"uPlot.iife.min.js": "19c8d4c6ad88929a79f4ae49d6f7161566dfd0ba3d15cc495e974f787eb78f1f",
		"uPlot.min.css":     "df630c6a8d6f8eeaff264b50f73ce5b114f646ffd9a0bb74f049b0a00135fa04",
		"LICENSE":           "8f989229699b4fe2f1a0432d0e9edc338a8a911e250e2d1b01ecd770a5f5b1bd",
	}
	for _, f := range m.Files {
		b, err := fs.ReadFile(assets, "vendor/uplot/"+f.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 || got != pins[f.Path] {
			t.Errorf("%s: sha256 %s, manifest %s, upstream %s", f.Path, got, f.SHA256, pins[f.Path])
		}
		delete(pins, f.Path)
	}
	if len(pins) != 0 {
		t.Errorf("the manifest leaves out %v", pins)
	}
	if len(vendor.Scripts) != 1 || !strings.HasPrefix(string(vendor.Scripts[0].Body), "/*! https://github.com/leeoniya/uPlot (v"+m.Version+") */") {
		t.Error("the vendored script's banner does not name the manifest's version")
	}
	if hashOf(string(vendor.Scripts[0].Body)) != vendor.Scripts[0].Hash || !strings.Contains(CSP, vendor.Scripts[0].Hash) {
		t.Error("the CSP does not list the vendored script's hash")
	}
	licence, err := fs.ReadFile(assets, "vendor/uplot/LICENSE")
	if err != nil || !strings.HasPrefix(string(licence), "The MIT License (MIT)") {
		t.Fatalf("the licence file is not MIT's: %v", err)
	}
	notices, err := os.ReadFile(filepath.Join("..", "..", "THIRD_PARTY_NOTICES.md"))
	if err != nil {
		t.Fatal(err)
	}
	n := strings.ReplaceAll(string(notices), "\r\n", "\n")
	if !strings.Contains(n, "uPlot 1.6.32") || !strings.Contains(n, "internal/web/vendor/uplot/") || !strings.Contains(n, strings.TrimSpace(string(licence))) {
		t.Error("THIRD_PARTY_NOTICES.md does not name uPlot 1.6.32, its directory and its licence's text")
	}
}

// TestVendorRefusals: loadVendor refuses a changed byte, an unlisted file, a listed file that is missing, a licence
// not allowed, a licence file not listed, a manifest field it does not know, a newer schema, a path that is not a
// plain name and a script holding </script, and loads a well-formed package.
func TestVendorRefusals(t *testing.T) {
	sum := func(s string) string {
		b := sha256.Sum256([]byte(s))
		return hex.EncodeToString(b[:])
	}
	manifest := func(license, extra, files string) string {
		return `{"schema":"guildhall.vendor-manifest/1","name":"x","version":"1.0.0","license":"` + license + `","license_file":"LICENSE",` +
			`"homepage":"","source":"https://example.invalid/x.tgz","source_sha1":"","source_integrity":""` + extra + `,"files":[` + files + `]}`
	}
	file := func(path, use, body string) string {
		return `{"path":"` + path + `","upstream":"package/` + path + `","use":"` + use + `","sha256":"` + sum(body) + `"}`
	}
	good := file("x.js", "script", "var x=1") + "," + file("x.css", "style", ".x{}") + "," + file("LICENSE", "license", "MIT")
	base := func() fstest.MapFS {
		return fstest.MapFS{
			"vendor/x/x.js":          {Data: []byte("var x=1")},
			"vendor/x/x.css":         {Data: []byte(".x{}")},
			"vendor/x/LICENSE":       {Data: []byte("MIT")},
			"vendor/x/manifest.json": {Data: []byte(manifest("MIT", "", good))},
		}
	}
	v, err := loadVendor(base())
	if err != nil || len(v.Scripts) != 1 || v.Scripts[0].Name != "x.js" || v.Scripts[0].Hash != hashOf("var x=1") || v.Style != ".x{}\n" {
		t.Fatalf("a well-formed package: %+v, %v", v, err)
	}
	for _, c := range []struct {
		name string
		edit func(fstest.MapFS)
		want string
	}{
		{"changed byte", func(f fstest.MapFS) { f["vendor/x/x.js"] = &fstest.MapFile{Data: []byte("var x=2")} }, "sha256 pin"},
		{"unlisted file", func(f fstest.MapFS) { f["vendor/x/extra.js"] = &fstest.MapFile{Data: []byte("1")} }, "not listed"},
		{"missing file", func(f fstest.MapFS) { delete(f, "vendor/x/x.css") }, "x.css"},
		{"GPL", func(f fstest.MapFS) { f["vendor/x/manifest.json"].Data = []byte(manifest("GPL-3.0", "", good)) }, "licence"},
		{"licence not listed", func(f fstest.MapFS) {
			f["vendor/x/manifest.json"].Data = []byte(manifest("MIT", "", file("x.js", "script", "var x=1")+","+file("x.css", "style", ".x{}")))
			delete(f, "vendor/x/LICENSE")
		}, "licence file"},
		{"unknown field", func(f fstest.MapFS) { f["vendor/x/manifest.json"].Data = []byte(manifest("MIT", `,"extra":1`, good)) }, "extra"},
		{"newer schema", func(f fstest.MapFS) {
			f["vendor/x/manifest.json"].Data = []byte(strings.Replace(manifest("MIT", "", good), "manifest/1", "manifest/2", 1))
		}, "newer"},
		{"path", func(f fstest.MapFS) {
			f["vendor/x/manifest.json"].Data = []byte(manifest("MIT", "", good+","+file("../y.js", "script", "1")))
		}, "plain name"},
		{"closing tag", func(f fstest.MapFS) {
			f["vendor/x/x.js"] = &fstest.MapFile{Data: []byte("'</SCRIPT>'")}
			f["vendor/x/manifest.json"].Data = []byte(manifest("MIT", "", file("x.js", "script", "'</SCRIPT>'")+","+file("x.css", "style", ".x{}")+","+file("LICENSE", "license", "MIT")))
		}, "</script"},
	} {
		f := base()
		c.edit(f)
		if _, err := loadVendor(f); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", c.name, err, c.want)
		}
	}
}
