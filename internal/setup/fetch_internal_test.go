package setup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// archiveFile writes a gzipped tar archive of the named regular files, each holding its own name, under top/, and
// returns its path.
func archiveFile(t *testing.T, names ...string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: "top/" + n, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(n))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// installed makes dest an earlier install holding marker, and returns its files and contents.
func installed(t *testing.T) (dest string, contents map[string]string) {
	t.Helper()
	dest = filepath.Join(t.TempDir(), "payload")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "marker"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dest, files(t, dest)
}

// files lists every file under dir with its contents.
func files(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// leftovers lists the scratch directories and old copies beside dest.
func leftovers(t *testing.T, dest string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".setup-unpack-") || strings.HasPrefix(e.Name(), filepath.Base(dest)+".old-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestUnpackScratchFailureKeepsEarlierInstall(t *testing.T) {
	dest, before := installed(t)
	archive := archiveFile(t, "new")
	boom := errors.New("no scratch directory")
	defer func(orig func(string, string) (string, error)) { mkdirTemp = orig }(mkdirTemp)
	mkdirTemp = func(string, string) (string, error) { return "", boom }
	if err := unpackInto(archive, "tar.gz", dest, 1); !errors.Is(err, boom) {
		t.Fatalf("unpackInto = %v, want %v", err, boom)
	}
	if after := files(t, dest); !reflect.DeepEqual(after, before) {
		t.Errorf("earlier install changed: before %v, after %v", before, after)
	}
}

func TestUnpackFailedSwapPutsEarlierInstallBack(t *testing.T) {
	dest, before := installed(t)
	archive := archiveFile(t, "new")
	boom := errors.New("cannot move into place")
	defer func(orig func(string, string) error) { rename = orig }(rename)
	orig := rename
	rename = func(from, to string) error {
		if to == dest && strings.HasPrefix(filepath.Base(from), ".setup-unpack-") {
			return boom
		}
		return orig(from, to)
	}
	err := unpackInto(archive, "tar.gz", dest, 1)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), dest) || !strings.Contains(err.Error(), dest+".old-1") {
		t.Fatalf("unpackInto = %v, want %v naming %s and its old copy", err, boom, dest)
	}
	if after := files(t, dest); !reflect.DeepEqual(after, before) {
		t.Errorf("earlier install not put back: before %v, after %v", before, after)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestUnpackReplacesEarlierInstall(t *testing.T) {
	dest, _ := installed(t)
	stale := dest + ".old-1"
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "crash"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unpackInto(archiveFile(t, "new"), "tar.gz", dest, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := files(t, dest), map[string]string{"new": "new"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dest = %v, want %v", got, want)
	}
	if got, want := files(t, stale), map[string]string{"crash": "kept"}; !reflect.DeepEqual(got, want) {
		t.Errorf("leftover from a crash = %v, want %v", got, want)
	}
	if l := leftovers(t, dest); !reflect.DeepEqual(l, []string{"payload.old-1"}) {
		t.Errorf("beside dest = %v, want only payload.old-1", l)
	}
}

// zipFile writes a zip archive of the named entries, each a file holding its own name or, ending in '/', a
// directory, and returns its path.
func zipFile(t *testing.T, names ...string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(n, "/") {
			if _, err := w.Write([]byte(n)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestUnpackZip: a zip, as Go's Windows release is, unpacks like a tar.gz, dropping strip leading elements and
// replacing an earlier install.
func TestUnpackZip(t *testing.T) {
	dest, _ := installed(t)
	if err := unpackInto(zipFile(t, "go/", "go/bin/", "go/bin/go.exe", "go/VERSION"), "zip", dest, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := files(t, dest), map[string]string{"bin/go.exe": "go/bin/go.exe", "VERSION": "go/VERSION"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dest = %v, want %v", got, want)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

// TestUnpackZipRefusesEscapes: a zip entry that leaves the destination, by "..", an absolute name, a '\' or a drive,
// is refused and the earlier install is kept.
func TestUnpackZipRefusesEscapes(t *testing.T) {
	for _, bad := range []string{"go/../../evil", "/evil", `go\..\evil`, "C:/evil"} {
		dest, before := installed(t)
		err := unpackInto(zipFile(t, "go/ok", bad), "zip", dest, 0)
		if err == nil || !strings.Contains(err.Error(), "leaves the destination") {
			t.Errorf("%q: unpackInto = %v, want it refused", bad, err)
		}
		if after := files(t, dest); !reflect.DeepEqual(after, before) {
			t.Errorf("%q: earlier install changed: before %v, after %v", bad, before, after)
		}
	}
}
