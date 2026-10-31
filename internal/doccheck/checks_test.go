package doccheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goodRecord returns a valid record body with the given extra header lines.
func goodRecord(title string, extra ...string) string {
	head := []string{"---", "status: accepted", "date: 2026-10-30", "topic: delivery"}
	head = append(head, extra...)
	head = append(head, "---", "", "# "+title, "", "## Context", "", "Why.", "", "## Decision", "", "What.", "", "## Consequences", "", "Cost.", "")
	return strings.Join(head, "\n")
}

// writeTree writes files under a new temporary root and returns it.
// Keys are slash-separated paths relative to the root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// lib names a record file in a fixture tree.
func lib(name string) string { return "docs/" + libDir + "/" + name }

func TestLibraryChecks(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string // substring of one reported problem; empty means no problem
	}{
		{"valid", map[string]string{lib("a.md"): goodRecord("A")}, ""},
		{"missing header", map[string]string{lib("a.md"): "# A\n\n## Context\n\n## Decision\n\n## Consequences\n"}, "front matter"},
		{"unknown status", map[string]string{lib("a.md"): strings.Replace(goodRecord("A"), "status: accepted", "status: proposed", 1)}, `unknown status "proposed"`},
		{"unknown topic", map[string]string{lib("a.md"): strings.Replace(goodRecord("A"), "topic: delivery", "topic: misc", 1)}, `unknown topic "misc"`},
		{"bad date", map[string]string{lib("a.md"): strings.Replace(goodRecord("A"), "2026-10-30", "30 Oct", 1)}, "date"},
		{"unknown key", map[string]string{lib("a.md"): goodRecord("A", "owner: me")}, `unknown header key "owner"`},
		{"no title", map[string]string{lib("a.md"): strings.Replace(goodRecord("A"), "# A", "A", 1)}, "title"},
		{"missing section", map[string]string{lib("a.md"): strings.Replace(goodRecord("A"), "## Consequences", "## Results", 1)}, "## Consequences"},
		{"numbered name", map[string]string{lib("0001-a.md"): goodRecord("A")}, "slug"},
		{"upper-case name", map[string]string{lib("A_b.md"): goodRecord("A")}, "slug"},
		{"one-sided supersede", map[string]string{
			lib("old.md"): goodRecord("Old"),
			lib("new.md"): goodRecord("New", "supersedes: old.md"),
		}, "old.md does not name new.md in superseded-by"},
		{"one-sided amend", map[string]string{
			lib("old.md"): goodRecord("Old", "amended-by: new.md"),
			lib("new.md"): goodRecord("New"),
		}, "new.md does not name old.md in amends"},
		{"superseded-by needs status", map[string]string{
			lib("old.md"): goodRecord("Old", "superseded-by: new.md"),
			lib("new.md"): goodRecord("New", "supersedes: old.md"),
		}, "status must be superseded"},
		{"superseded needs successor", map[string]string{
			lib("old.md"): strings.Replace(goodRecord("Old"), "accepted", "superseded", 1),
		}, "names no superseded-by"},
		{"relation to missing file", map[string]string{lib("new.md"): goodRecord("New", "amends: gone.md")}, "gone.md does not exist"},
		{"valid supersede pair", map[string]string{
			lib("old.md"): strings.Replace(goodRecord("Old", "superseded-by: new.md"), "accepted", "superseded", 1),
			lib("new.md"): goodRecord("New", "supersedes: old.md"),
		}, ""},
		{"formerly twice", map[string]string{
			lib("a.md"): goodRecord("A", "formerly: D1"),
			lib("b.md"): goodRecord("B", "formerly: D1"),
		}, "former name D1 appears twice"},
		{"formerly names a live file", map[string]string{
			lib("a.md"): goodRecord("A", "formerly: b.md"),
			lib("b.md"): goodRecord("B"),
		}, "former name b.md is a live record"},
		{"bad formerly value", map[string]string{lib("a.md"): goodRecord("A", "formerly: see D1")}, "former name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeTree(t, c.files)
			recs, problems := loadLibrary(filepath.Join(root, "docs", libDir))
			problems = append(problems, checkRelations(recs)...)
			problems = append(problems, checkFormerly(recs, nil)...)
			assertProblem(t, problems, c.want)
		})
	}
}

func TestIndexGeneration(t *testing.T) {
	root := writeTree(t, map[string]string{
		lib("b.md"):   goodRecord("B decides", "formerly: D2, 0002-b.md"),
		lib("a.md"):   goodRecord("A decides", "formerly: D10"),
		lib("old.md"): strings.Replace(goodRecord("Old decides", "superseded-by: a.md", "formerly: D1"), "accepted", "superseded", 1),
		lib("d.md"):   strings.Replace(strings.Replace(goodRecord("D designs"), "accepted", "designed", 1), "topic: delivery", "topic: foundation", 1),
	})
	recs, problems := loadLibrary(filepath.Join(root, "docs", libDir))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	// a.md does not name old.md in supersedes, but generation does not check relations.
	got := string(generateIndex("# Index\n\n"+marker+"\n", recs, map[string]movedFile{
		"gone.md": {Target: "test-timing.md#gone", Label: "Test timing"},
	}))
	want := strings.Join([]string{
		"# Index",
		"",
		marker,
		"",
		"## " + topicTitle("foundation"),
		"",
		"- [D designs](" + libDir + "/d.md) (designed, not built)",
		"",
		"## " + topicTitle("delivery"),
		"",
		"- [A decides](" + libDir + "/a.md)",
		"- [B decides](" + libDir + "/b.md)",
		"",
		"## Superseded",
		"",
		"- [Old decides](" + libDir + "/old.md), superseded by [A decides](" + libDir + "/a.md)",
		"",
		"## Former names",
		"",
		"| Former name | Now in |",
		"|---|---|",
		"| D1 | [Old decides](" + libDir + "/old.md) |",
		"| D2 | [B decides](" + libDir + "/b.md) |",
		"| D10 | [A decides](" + libDir + "/a.md) |",
		"| 0002-b.md | [B decides](" + libDir + "/b.md) |",
		"| gone.md | [Test timing](test-timing.md#gone) |",
		"",
	}, "\n")
	if got != want {
		t.Errorf("generated index:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndexStale(t *testing.T) {
	root := writeTree(t, map[string]string{
		lib("a.md"):              goodRecord("A decides"),
		"docs/" + libDir + ".md": "# Index\n\n" + marker + "\n\n## Delivery\n\n- [Old line](" + libDir + "/a.md)\n",
	})
	_, problems := checkIndex(root, nil, false)
	assertProblem(t, problems, regenerate)
}

func TestIndexNeedsMarker(t *testing.T) {
	root := writeTree(t, map[string]string{
		lib("a.md"):              goodRecord("A decides"),
		"docs/" + libDir + ".md": "# Index\n",
	})
	_, problems := checkIndex(root, nil, false)
	assertProblem(t, problems, "marker")
}

func TestLinkCheck(t *testing.T) {
	path := "docs/" + libDir + "/"
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"live links", map[string]string{
			lib("a.md"):             goodRecord("A") + "See [b](b.md) and [cli](../contracts/cli.md#x).\n",
			lib("b.md"):             goodRecord("B"),
			"docs/contracts/cli.md": "Per [a](../" + libDir + "/a.md#why).\n",
			"internal/x/x.go":       "// See " + path + "a.md.\npackage x\n",
			"README.md":             "Read " + path + "b.md.\n",
		}, ""},
		{"dead go path", map[string]string{"internal/x/x.go": "// See " + path + "gone.md.\npackage x\n"}, "internal/x/x.go:1: " + path + "gone.md"},
		{"dead relative link", map[string]string{"docs/contracts/cli.md": "x\nPer [a](../" + libDir + "/gone.md).\n"}, "docs/contracts/cli.md:2"},
		{"wrong relative prefix", map[string]string{
			lib("a.md"):             goodRecord("A"),
			"docs/contracts/cli.md": "Per [a](" + libDir + "/a.md).\n",
		}, "docs/contracts/cli.md:1"},
		{"dead link between records", map[string]string{lib("a.md"): goodRecord("A") + "See [b](b.md).\n"}, lib("a.md")},
		{"dead link in a skill", map[string]string{"skills/s/SKILL.md": "See " + path + "gone.md.\n"}, "skills/s/SKILL.md:1"},
		{"untracked root note is skipped", map[string]string{"notes.md": "See " + path + "gone.md.\n"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := writeTree(t, c.files)
			assertProblem(t, checkLinks(root), c.want)
		})
	}
}

func assertProblem(t *testing.T, problems []string, want string) {
	t.Helper()
	if want == "" {
		if len(problems) > 0 {
			t.Errorf("want no problems, got:\n%s", strings.Join(problems, "\n"))
		}
		return
	}
	for _, p := range problems {
		if strings.Contains(p, want) {
			return
		}
	}
	t.Errorf("want a problem containing %q, got:\n%s", want, strings.Join(problems, "\n"))
}
