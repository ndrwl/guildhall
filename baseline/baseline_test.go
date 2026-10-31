package baseline_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var update = flag.Bool("update", false, "rewrite the digest goldens")

// goldenDir mirrors the embedded tree with one sha256sum-format <path>.sha256 file per embedded file.
const goldenDir = "testdata/digests"

// files lists every embedded file by slash path, sorted.
func files(t *testing.T) []string {
	t.Helper()
	var names []string
	err := fs.WalkDir(baseline.Release(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

func TestReleaseHoldsTheRolesAndAGuidePerArtisanKind(t *testing.T) {
	want := []string{"roles/artisan.md", "roles/director.md", "roles/judge.md", "roles/steward.md"}
	for _, k := range workflow.Kinds() {
		if slices.Contains(k.Actors, workflow.ArtisanActor) {
			want = append(want, "stages/"+k.Type+".md")
		}
	}
	slices.Sort(want)
	if got := files(t); !slices.Equal(got, want) {
		t.Errorf("release baseline = %q, want %q", got, want)
	}
	if _, err := fs.Stat(baseline.Release(), "standards"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("release baseline ships standards: %v", err)
	}
}

// sentenceBreak finds a sentence end followed by another sentence on the same line.
var sentenceBreak = regexp.MustCompile("[.!?][\"')`*_]*\\s+[\"'(`*_]*[A-Z]")

// listMarker is a Markdown list item's marker, whose "1." is not a sentence end.
var listMarker = regexp.MustCompile(`^\s*([-*+]|\d+\.)\s+`)

func TestContentIsPlainMarkdown(t *testing.T) {
	for _, p := range files(t) {
		data, err := fs.ReadFile(baseline.Release(), p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		switch {
		case strings.HasPrefix(text, "---"):
			t.Errorf("%s: has frontmatter", p)
		case bytes.ContainsRune(data, '\r'):
			t.Errorf("%s: has a CR; content is LF only", p)
		case !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n"):
			t.Errorf("%s: must end with exactly one newline", p)
		}
		for i, line := range strings.Split(text, "\n") {
			if sentenceBreak.MatchString(listMarker.ReplaceAllString(line, "")) {
				t.Errorf("%s: line %d has more than one sentence: %q", p, i+1, line)
			}
		}
	}
}

// digests is the sha256 of every embedded file, by slash path.
func digests(t *testing.T) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, p := range files(t) {
		data, err := fs.ReadFile(baseline.Release(), p)
		if err != nil {
			t.Fatal(err)
		}
		got[p] = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	return got
}

// hexDigest is a sha256 as sha256sum prints it.
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// readGolden reads a golden directory mirroring the embedded tree: one <path>.sha256 file per embedded file,
// holding its "<hex>  <path>" sha256sum line.
// It does not trim a CR, so a CRLF checkout fails here rather than passing unnoticed.
func readGolden(fsys fs.FS) (map[string]string, error) {
	want := map[string]string{}
	err := fs.WalkDir(fsys, ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		p, ok := strings.CutSuffix(file, ".sha256")
		if !ok {
			return fmt.Errorf("%s: not a .sha256 golden", file)
		}
		data, err := fs.ReadFile(fsys, file)
		if err != nil {
			return err
		}
		line, ok := strings.CutSuffix(string(data), "\n")
		hexsum, name, cut := strings.Cut(line, "  ")
		if !ok || !cut || strings.Contains(line, "\n") || !hexDigest.MatchString(hexsum) {
			return fmt.Errorf("%s: want one %q line, got %q", file, "<hex>  <path>", data)
		}
		if name != p {
			return fmt.Errorf("%s: records path %q, want %q", file, name, p)
		}
		want[p] = hexsum
		return nil
	})
	return want, err
}

// checkGolden compares the golden directory fsys with each embedded file's digest in got:
// a file with no golden, an orphan golden and a differing digest each fail.
func checkGolden(fsys fs.FS, got map[string]string) error {
	golden, err := readGolden(fsys)
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range slices.Sorted(maps.Keys(got)) {
		want, ok := golden[p]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("%s has no golden %s.sha256", p, p))
		case got[p] != want:
			errs = append(errs, fmt.Errorf("%s: digest %s, golden %s (a CRLF checkout changes it: see .gitattributes)",
				p, got[p], want))
		}
	}
	for _, p := range slices.Sorted(maps.Keys(golden)) {
		if _, ok := got[p]; !ok {
			errs = append(errs, fmt.Errorf("golden %s.sha256 names no embedded file", p))
		}
	}
	return errors.Join(errs...)
}

// writeGolden writes one <path>.sha256 file per embedded file of got under dir and removes any orphan .sha256 file.
func writeGolden(dir string, got map[string]string) error {
	for p, hexsum := range got {
		file := filepath.Join(dir, filepath.FromSlash(p)+".sha256")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, fmt.Appendf(nil, "%s  %s\n", hexsum, p), 0o644); err != nil {
			return err
		}
	}
	return fs.WalkDir(os.DirFS(dir), ".", func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if p, ok := strings.CutSuffix(file, ".sha256"); ok {
			if _, kept := got[p]; !kept {
				return os.Remove(filepath.Join(dir, filepath.FromSlash(file)))
			}
		}
		return nil
	})
}

func TestGoldenRefusesAMissingOrphanOrMalformedFile(t *testing.T) {
	const a, b = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	const role, stage = "roles/one.md.sha256", "stages/two.md.sha256"
	good := fstest.MapFS{role: file(a + "  roles/one.md\n"), stage: file(b + "  stages/two.md\n")}
	got := map[string]string{"roles/one.md": a, "stages/two.md": b}
	if err := checkGolden(good, got); err != nil {
		t.Fatalf("matching golden: %v", err)
	}
	for _, tc := range []struct {
		name string
		fsys fstest.MapFS
		got  map[string]string
		want string
	}{
		{"missing file", fstest.MapFS{role: good[role]}, got, "stages/two.md has no golden stages/two.md.sha256"},
		{"orphan file", good, map[string]string{"roles/one.md": a}, "golden stages/two.md.sha256 names no embedded file"},
		{"two lines", fstest.MapFS{role: file(a + "  roles/one.md\n" + a + "  roles/one.md\n")}, got, role + ": want one"},
		{"no separator", fstest.MapFS{role: file(a + " roles/one.md\n")}, got, role + ": want one"},
		{"bad hex", fstest.MapFS{role: file("xyz  roles/one.md\n")}, got, role + ": want one"},
		{"no newline", fstest.MapFS{role: file(a + "  roles/one.md")}, got, role + ": want one"},
		{"CRLF", fstest.MapFS{role: file(a + "  roles/one.md\r\n")}, got, role + `: records path "roles/one.md\r"`},
		{"flat name", fstest.MapFS{role: file(a + "  one.md\n")}, got, role + `: records path "one.md", want "roles/one.md"`},
		{"not a golden", fstest.MapFS{"roles/one.md": file(a + "  roles/one.md\n")}, got, "roles/one.md: not a .sha256 golden"},
		{"other digest", good, map[string]string{"roles/one.md": b, "stages/two.md": b}, "roles/one.md: digest " + b},
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
	if err := os.MkdirAll(filepath.Join(dir, "stages"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stages", "gone.md.sha256"), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{"roles/one.md": strings.Repeat("a", 64), "stages/two.md": strings.Repeat("b", 64)}
	if err := writeGolden(dir, got); err != nil {
		t.Fatal(err)
	}
	if err := checkGolden(os.DirFS(dir), got); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stages", "gone.md.sha256")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("orphan kept: %v", err)
	}
}

func TestDigestsMatchGolden(t *testing.T) {
	got := digests(t)
	if *update {
		if err := writeGolden(goldenDir, got); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkGolden(os.DirFS(goldenDir), got); err != nil {
		t.Error(err)
	}
}

func TestInstallWritesTheReleaseByteForByte(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "baseline")
	if err := baseline.Install(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range files(t) {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		want, _ := fs.ReadFile(baseline.Release(), p)
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from the release", p)
		}
	}
	if err := baseline.Install(dir); !errors.Is(err, baseline.ErrNotEmpty) {
		t.Errorf("second install: %v, want ErrNotEmpty", err)
	}
}
