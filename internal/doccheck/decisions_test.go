// Package doccheck holds the tests that keep the decision library, its generated index and every link into it consistent.
// The scheme they check is described at the top of docs/decisions.md.
package doccheck

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// libDir is the library's directory under docs/, and docs/<libDir>.md is its index.
	libDir = "decisions"
	// marker ends the hand-written convention text of the index; everything after it is generated.
	marker = "<!-- Generated from the records' headers below this line: do not edit by hand. -->"
	// regenerate is the command that rewrites the generated part of the index.
	regenerate = "go test ./internal/doccheck -run TestDecisionIndex -update"
)

// topics lists every topic a record may name, in the index's order, with its heading.
// Adding a topic is a one-line change here, which review sees.
var topics = []struct{ key, title string }{
	{"foundation", "Foundation and working method"},
	{"dependencies", "Dependencies"},
	{"ci", "Continuous integration"},
	{"release", "Releases and the baseline"},
	{"store", "Store"},
	{"cli", "Command line"},
	{"workflow", "Workflows and their binding"},
	{"engine", "Engine, launcher and watcher"},
	{"containment", "Containment and attempt ownership"},
	{"endpoint", "Attempt endpoint"},
	{"runtimes", "Runtimes and adapters"},
	{"grants", "Grants, profiles and permissions"},
	{"herdr", "Herdr and presentation"},
	{"artisan", "Artisans"},
	{"review", "Review"},
	{"quality", "Quality"},
	{"delivery", "Delivery, merging and guard rails"},
	{"tasks", "Tasks and holds"},
	{"director", "Director"},
	{"messaging", "Messaging"},
	{"maintenance", "Maintenance and hallmarks"},
	{"steward", "Steward"},
}

var statuses = []string{"accepted", "designed", "superseded", "rejected"}

// headerKeys lists the front matter keys a record may use.
var headerKeys = []string{"status", "date", "topic", "tasks", "supersedes", "amends", "superseded-by", "amended-by", "formerly"}

// requiredSections are the body headings every record has.
var requiredSections = []string{"## Context", "## Decision", "## Consequences"}

// lastNumber is the last number the retired numbering assigned; every D1 to D<lastNumber> must resolve.
const lastNumber = 96

var (
	slugName     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.md$`)
	numberedName = regexp.MustCompile(`^[0-9]{4}-`)
	formerName   = regexp.MustCompile(`^(D[1-9][0-9]*|[a-z0-9]+(-[a-z0-9]+)*\.md)$`)
	taskID       = regexp.MustCompile(`^[a-z0-9]+(/[a-z0-9-]+)?$`)
)

func topicTitle(key string) string {
	for _, t := range topics {
		if t.key == key {
			return t.title
		}
	}
	return ""
}

// record is one parsed decision record.
type record struct {
	file   string // file name within the library, such as delivery-stage.md
	title  string // the "# " line without its marker
	status string
	date   string
	topic  string
	lists  map[string][]string // the comma-separated fields: tasks, supersedes, amends, superseded-by, amended-by, formerly
}

// movedFile is where a former record that is no longer a record now lives, as a link from docs/.
type movedFile struct{ Target, Label string }

// parseRecord parses one record and reports every fault in it, each prefixed with its file name.
func parseRecord(name string, data []byte) (record, []string) {
	r := record{file: name, lists: map[string][]string{}}
	var problems []string
	fault := func(format string, a ...any) { problems = append(problems, name+": "+fmt.Sprintf(format, a...)) }
	if !slugName.MatchString(name) || numberedName.MatchString(name) {
		fault("file name is not a slug matching %s with no NNNN- number", slugName)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		fault("does not start with front matter (a --- line)")
		return r, problems
	}
	end := slices.Index(lines[1:], "---")
	if end < 0 {
		fault("front matter has no closing --- line")
		return r, problems
	}
	end++
	seen := map[string]bool{}
	for _, line := range lines[1:end] {
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case !ok:
			fault("front matter line %q is not key: value", line)
			continue
		case !slices.Contains(headerKeys, key):
			fault("unknown header key %q", key)
			continue
		case seen[key]:
			fault("header key %q appears twice", key)
			continue
		}
		seen[key] = true
		switch key {
		case "status":
			r.status = value
		case "date":
			r.date = value
		case "topic":
			r.topic = value
		default:
			for _, v := range strings.Split(value, ",") {
				if v = strings.TrimSpace(v); v != "" {
					r.lists[key] = append(r.lists[key], v)
				}
			}
		}
	}
	if !slices.Contains(statuses, r.status) {
		fault("unknown status %q; use one of %s", r.status, strings.Join(statuses, ", "))
	}
	if topicTitle(r.topic) == "" {
		fault("unknown topic %q; add it to the topics list in internal/doccheck if it is new", r.topic)
	}
	if _, err := time.Parse(time.DateOnly, r.date); err != nil {
		fault("date %q is not YYYY-MM-DD", r.date)
	}
	for _, id := range r.lists["tasks"] {
		if !taskID.MatchString(id) {
			fault("task id %q is not a Guildhall task id", id)
		}
	}
	for _, f := range r.lists["formerly"] {
		if !formerName.MatchString(f) {
			fault("former name %q is neither Dn nor a file name", f)
		}
	}
	body := lines[end+1:]
	for _, line := range body {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if t, ok := strings.CutPrefix(line, "# "); ok {
			r.title = strings.TrimSpace(t)
		}
		break
	}
	if r.title == "" {
		fault("the first line after the front matter is not a # title")
	}
	for _, s := range requiredSections {
		if !slices.Contains(body, s) {
			fault("has no %q section", s)
		}
	}
	return r, problems
}

// loadLibrary parses every record in dir, sorted by file name.
func loadLibrary(dir string) ([]record, []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, []string{err.Error()}
	}
	var recs []record
	var problems []string
	for _, e := range entries {
		if e.IsDir() {
			problems = append(problems, e.Name()+": the library holds no directories")
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		r, p := parseRecord(e.Name(), data)
		recs = append(recs, r)
		problems = append(problems, p...)
	}
	return recs, problems
}

// inverse pairs each relation with the field that must point back.
var inverse = map[string]string{
	"supersedes":    "superseded-by",
	"superseded-by": "supersedes",
	"amends":        "amended-by",
	"amended-by":    "amends",
}

// checkRelations checks that every supersede and amend relation names an existing record that points back.
func checkRelations(recs []record) []string {
	byName := map[string]record{}
	for _, r := range recs {
		byName[r.file] = r
	}
	var problems []string
	for _, r := range recs {
		for _, key := range []string{"supersedes", "superseded-by", "amends", "amended-by"} {
			for _, other := range r.lists[key] {
				o, ok := byName[other]
				switch {
				case other == r.file:
					problems = append(problems, fmt.Sprintf("%s: %s names the record itself", r.file, key))
				case !ok:
					problems = append(problems, fmt.Sprintf("%s: %s %s does not exist", r.file, key, other))
				case !slices.Contains(o.lists[inverse[key]], r.file):
					problems = append(problems, fmt.Sprintf("%s: %s %s, but %s does not name %s in %s", r.file, key, other, other, r.file, inverse[key]))
				}
			}
		}
		superseded := len(r.lists["superseded-by"]) > 0
		if superseded && r.status != "superseded" {
			problems = append(problems, fmt.Sprintf("%s: has superseded-by, so its status must be superseded, not %q", r.file, r.status))
		}
		if r.status == "superseded" && !superseded {
			problems = append(problems, fmt.Sprintf("%s: status superseded but names no superseded-by", r.file))
		}
	}
	return problems
}

// checkFormerly checks that former names are unique, never a live record's name, and never a moved file's name too.
func checkFormerly(recs []record, moved map[string]movedFile) []string {
	live := map[string]bool{}
	for _, r := range recs {
		live[r.file] = true
	}
	owner := map[string]string{}
	for name := range moved {
		owner[name] = "the moved-files list"
	}
	var problems []string
	for _, r := range recs {
		for _, f := range r.lists["formerly"] {
			if prev, ok := owner[f]; ok {
				problems = append(problems, fmt.Sprintf("former name %s appears twice: in %s and %s", f, prev, r.file))
			}
			owner[f] = r.file
			if live[f] {
				problems = append(problems, fmt.Sprintf("%s: former name %s is a live record's file name", r.file, f))
			}
		}
	}
	for name := range moved {
		if live[name] {
			problems = append(problems, fmt.Sprintf("moved file %s is still a live record", name))
		}
	}
	return problems
}

// checkNumbers checks that every number the retired scheme assigned resolves through a former name.
func checkNumbers(recs []record) []string {
	have := map[string]bool{}
	for _, r := range recs {
		for _, f := range r.lists["formerly"] {
			have[f] = true
		}
	}
	var problems []string
	for n := 1; n <= lastNumber; n++ {
		if !have["D"+strconv.Itoa(n)] {
			problems = append(problems, fmt.Sprintf("D%d is in no record's formerly field", n))
		}
	}
	return problems
}

// generateIndex returns the index: the hand-written head up to and including the marker line, then the generated part.
func generateIndex(head string, recs []record, moved map[string]movedFile) []byte {
	var b bytes.Buffer
	b.WriteString(head)
	byName := map[string]record{}
	for _, r := range recs {
		byName[r.file] = r
	}
	sorted := slices.Clone(recs)
	slices.SortFunc(sorted, func(a, b record) int { return strings.Compare(a.file, b.file) })
	link := func(r record) string { return fmt.Sprintf("[%s](%s/%s)", r.title, libDir, r.file) }
	for _, t := range topics {
		var lines []string
		for _, r := range sorted {
			if r.topic != t.key || r.status == "superseded" {
				continue
			}
			line := "- " + link(r)
			switch r.status {
			case "designed":
				line += " (designed, not built)"
			case "rejected":
				line += " (rejected)"
			}
			lines = append(lines, line)
		}
		if len(lines) > 0 {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", t.title, strings.Join(lines, "\n"))
		}
	}
	var superseded []string
	for _, r := range sorted {
		if r.status != "superseded" {
			continue
		}
		var by []string
		for _, s := range r.lists["superseded-by"] {
			if o, ok := byName[s]; ok {
				by = append(by, link(o))
			} else {
				by = append(by, s)
			}
		}
		superseded = append(superseded, fmt.Sprintf("- %s, superseded by %s", link(r), strings.Join(by, " and ")))
	}
	if len(superseded) > 0 {
		fmt.Fprintf(&b, "\n## Superseded\n\n%s\n", strings.Join(superseded, "\n"))
	}
	type row struct{ name, now string }
	var rows []row
	for _, r := range sorted {
		for _, f := range r.lists["formerly"] {
			rows = append(rows, row{f, link(r)})
		}
	}
	for name, m := range moved {
		rows = append(rows, row{name, fmt.Sprintf("[%s](%s)", m.Label, m.Target)})
	}
	number := func(s string) int {
		if n, err := strconv.Atoi(strings.TrimPrefix(s, "D")); err == nil && strings.HasPrefix(s, "D") {
			return n
		}
		return -1
	}
	slices.SortFunc(rows, func(a, b row) int {
		na, nb := number(a.name), number(b.name)
		switch {
		case na >= 0 && nb >= 0:
			return na - nb
		case na >= 0:
			return -1
		case nb >= 0:
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
	if len(rows) > 0 {
		b.WriteString("\n## Former names\n\n| Former name | Now in |\n|---|---|\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | %s |\n", r.name, r.now)
		}
	}
	return b.Bytes()
}

// checkIndex compares docs/<libDir>.md under root with the index generated from the library, and rewrites it when update is set.
// It returns the generated index and the problems found.
func checkIndex(root string, moved map[string]movedFile, update bool) ([]byte, []string) {
	indexPath := filepath.Join(root, "docs", libDir+".md")
	committed, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, []string{err.Error()}
	}
	text := strings.ReplaceAll(string(committed), "\r\n", "\n")
	at := strings.Index(text, marker+"\n")
	if at < 0 {
		return nil, []string{fmt.Sprintf("docs/%s.md has no generated-part marker line %q", libDir, marker)}
	}
	recs, _ := loadLibrary(filepath.Join(root, "docs", libDir))
	want := generateIndex(text[:at+len(marker)+1], recs, moved)
	if update {
		if err := os.WriteFile(indexPath, want, 0o644); err != nil {
			return want, []string{err.Error()}
		}
		return want, nil
	}
	if text != string(want) {
		return want, []string{fmt.Sprintf("docs/%s.md is stale; regenerate it with: %s", libDir, regenerate)}
	}
	return want, nil
}

// rootFiles are the repository-root files the link check reads; every other root file, such as an untracked note, is skipped.
var rootFiles = []string{"README.md", "CONTRIBUTING.md"}

var (
	libraryPath  = regexp.MustCompile(`docs/` + libDir + `/[A-Za-z0-9._-]+\.md`)
	markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)
)

// checkLinks finds every docs/<libDir>/<file>.md path in Go and Markdown files and every relative Markdown link into the library, and every relative Markdown link in a record, that names a missing file.
// It reads the root's rootFiles and every directory below the root except those whose names start with a dot.
func checkLinks(root string) []string {
	var files []string
	for _, f := range rootFiles {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			files = append(files, f)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return []string{err.Error()}
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		err := filepath.WalkDir(filepath.Join(root, e.Name()), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if !d.IsDir() && (strings.HasSuffix(p, ".go") || strings.HasSuffix(p, ".md")) {
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return []string{err.Error()}
		}
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		return err == nil
	}
	inLibrary := func(rel string) bool { return path.Dir(rel) == "docs/"+libDir }
	var problems []string
	for _, rel := range files {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			for _, p := range libraryPath.FindAllString(line, -1) {
				if !exists(p) {
					problems = append(problems, fmt.Sprintf("%s:%d: %s does not exist", rel, n, p))
				}
			}
			if !strings.HasSuffix(rel, ".md") {
				continue
			}
			for _, m := range markdownLink.FindAllStringSubmatch(line, -1) {
				target, _, _ := strings.Cut(m[1], "#")
				if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				resolved := path.Join(path.Dir(rel), target)
				if !inLibrary(rel) && !inLibrary(resolved) && !strings.Contains(target, libDir+"/") {
					continue
				}
				if !exists(resolved) {
					problems = append(problems, fmt.Sprintf("%s:%d: link %s resolves to %s, which does not exist", rel, n, m[1], resolved))
				}
			}
		}
		if err := sc.Err(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))
		}
		if err := f.Close(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: close: %v", rel, err))
		}
	}
	return problems
}
