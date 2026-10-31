package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

// retiredDigests pins the digest of each file in retired/, so a change to the definition structs or their JSON tags,
// which would leave a task bound under an earlier release unresolvable, fails here first.
var retiredDigests = map[string]string{
	"default-873af971.json": "sha256:51082f93ca86e72e3b937dad60cec7b13d8bbf73181f2d1b08ff4089ade9d7a7",
	"default-a10f8e3a.json": "sha256:30e722b79c1f796b1a64d743117298455e31ea53fde9ee078a8c838aa2d2706d",
	"default-48f5f5f3.json": "sha256:749fc78122944fc6212c7c648f59375028e7e228c1a71448e0aee70ab037aaea",
	"bug-8b2ac870.json":     "sha256:645353ff072051a490689e5e89cebc7a8e8af7ac7c25afdddcab6d26f3b6a3b8",
}

// TestRetiredRevisionsArePinned: every retired file parses, validates and keeps its pinned digest, and Retired
// returns each by its name and none by another.
func TestRetiredRevisionsArePinned(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("retired", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(retiredDigests) {
		t.Fatalf("retired/ holds %v, want exactly the pinned %v", files, retiredDigests)
	}
	want := map[string]map[string]bool{}
	for _, f := range files {
		base := filepath.Base(f)
		pinned, ok := retiredDigests[base]
		if !ok {
			t.Errorf("%s has no pinned digest", base)
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		def, err := Parse(data)
		if err != nil {
			t.Fatalf("%s: %v", base, err)
		}
		if err := Validate(def); err != nil {
			t.Errorf("%s: %v", base, err)
		}
		if got := def.Digest(); got != pinned {
			t.Errorf("%s digest = %s, pinned %s", base, got, pinned)
		}
		if want[def.Name] == nil {
			want[def.Name] = map[string]bool{}
		}
		want[def.Name][def.Digest()] = true
	}
	for _, name := range []string{"default", "bug", "no-such-workflow"} {
		defs, err := Retired(name)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, d := range defs {
			if d.Name != name {
				t.Errorf("Retired(%q) returned %q", name, d.Name)
			}
			got[d.Digest()] = true
		}
		if len(got) != len(want[name]) {
			t.Errorf("Retired(%q) = %v, want %v", name, got, want[name])
		}
		for d := range want[name] {
			if !got[d] {
				t.Errorf("Retired(%q) lacks %s", name, d)
			}
		}
	}
}

// TestShippedDigestsArePinned guards the next shipped edit: changing default.json or bug.json changes the digest
// every open task bound, which only a retired copy of the previous bytes keeps resolvable.
func TestShippedDigestsArePinned(t *testing.T) {
	for _, c := range []struct {
		file, digest string
		load         func() (*Definition, error)
	}{
		{"default.json", "sha256:3731dac936e4413444c9544c7efecea9ef002a1c44ca2b04059520773bf3d0a2", Default},
		{"bug.json", "sha256:f8e77aaf16708bbadf33703d19e2968582082f52f6efc273885f91b1813f3ff3", Bug},
	} {
		def, err := c.load()
		if err != nil {
			t.Fatal(err)
		}
		if got := def.Digest(); got != c.digest {
			t.Errorf("shipped %s digest = %s, pinned %s.\n"+
				"Open tasks bound the pinned revision and strand unless its bytes stay resolvable: copy the previous %s "+
				"bytes into internal/workflow/retired/%s-<commit>.json (git show <commit>:internal/workflow/%s), pin that "+
				"file's digest in retiredDigests, then update this constant (docs/decisions/retired-shipped-revisions.md).",
				c.file, got, c.digest, c.file, def.Name, c.file)
		}
	}
}
