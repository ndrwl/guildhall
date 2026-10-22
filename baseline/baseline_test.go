package baseline_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/baseline"
	"github.com/ndrwl/guildhall/internal/workflow"
)

var update = flag.Bool("update", false, "rewrite the digest golden")

const goldenPath = "testdata/digests.txt"

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
	want := []string{"roles/artisan.md", "roles/director.md"}
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

// digests is the sha256sum listing of every embedded file.
func digests(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, p := range files(t) {
		data, err := fs.ReadFile(baseline.Release(), p)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(data), p)
	}
	return b.String()
}

func TestDigestsMatchGolden(t *testing.T) {
	got := digests(t)
	if *update {
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("digests differ from %s (a CRLF checkout changes them: see .gitattributes):\n%s", goldenPath, got)
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
