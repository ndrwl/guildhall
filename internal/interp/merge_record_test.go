package interp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// recordReader serves documents from files by name, failing for a name it lacks, and counts what it read.
type recordReader struct {
	files map[string]string
	read  []string
}

func (r *recordReader) refs(names ...string) []store.DocumentRef {
	out := make([]store.DocumentRef, 0, len(names))
	for _, n := range names {
		out = append(out, store.DocumentRef{Repository: "app", TaskID: "t1", Name: n, Path: "tasks/app/t1/" + n})
	}
	return out
}

func (r *recordReader) get(d store.DocumentRef) ([]byte, error) {
	r.read = append(r.read, d.Name)
	data, ok := r.files[d.Name]
	if !ok {
		return nil, errors.New("no such file")
	}
	return []byte(data), nil
}

var (
	headA = strings.Repeat("a", 40)
	headB = strings.Repeat("b", 40)
)

func prRecord(head string) string {
	return `{"schema":"guildhall.pull-request/1","remote":"origin","number":3,"head":"` + head + `","merge_commit":"` + head + `"}`
}

// TestMergeRecord: none gives nil; one or two give the last in document order, read alone; a bad schema, an invalid
// head and a read error are errors naming the record.
func TestMergeRecord(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		docs  []string
		want  string // head, or the error's text
		err   bool
	}{
		{name: "none", docs: []string{"build-1-prepared", "deliver-1-pr"}},
		{name: "one", files: map[string]string{"deliver-1-merge": prRecord(headA)}, docs: []string{"deliver-1-pr", "deliver-1-merge"}, want: headA},
		{name: "two", files: map[string]string{"deliver-1-merge": prRecord(headA), "outside-3-merge": prRecord(headB)}, docs: []string{"deliver-1-merge", "outside-3-merge"}, want: headB},
		{name: "older bad", files: map[string]string{"deliver-1-merge": "{", "outside-3-merge": prRecord(headB)}, docs: []string{"deliver-1-merge", "outside-3-merge"}, want: headB},
		{name: "bad schema", files: map[string]string{"deliver-1-merge": `{"schema":"guildhall.other/1","head":"` + headA + `"}`}, docs: []string{"deliver-1-merge"}, want: "merge record deliver-1-merge is not a guildhall.pull-request/1 document", err: true},
		{name: "invalid head", files: map[string]string{"deliver-1-merge": `{"schema":"guildhall.pull-request/1","head":"nope"}`}, docs: []string{"deliver-1-merge"}, want: "merge record deliver-1-merge names no valid head", err: true},
		{name: "not JSON", files: map[string]string{"deliver-1-merge": "{"}, docs: []string{"deliver-1-merge"}, want: "merge record deliver-1-merge: ", err: true},
		{name: "read error", docs: []string{"deliver-1-merge"}, want: "merge record deliver-1-merge: no such file", err: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &recordReader{files: c.files}
			m, err := interp.MergeRecord(r.refs(c.docs...), r.get)
			switch {
			case c.err:
				if err == nil || !strings.HasPrefix(err.Error(), c.want) {
					t.Fatalf("MergeRecord = %+v, %v; want error %q", m, err, c.want)
				}
			case err != nil:
				t.Fatal(err)
			case c.want == "":
				if m != nil || len(r.read) != 0 {
					t.Fatalf("MergeRecord = %+v after reading %q, want nil and nothing read", m, r.read)
				}
			default:
				if m == nil || m.Head != c.want || m.Remote != "origin" || m.MergeCommit != c.want || m.Document != c.docs[len(c.docs)-1] {
					t.Fatalf("MergeRecord = %+v, want head %s from %s", m, c.want, c.docs[len(c.docs)-1])
				}
				if len(r.read) != 1 {
					t.Fatalf("read %q, want the newest record alone", r.read)
				}
			}
		})
	}
}

// TestSafeHead: a merge record's head wins over the finished document's; the finished head alone is used; an empty
// finished head and no document give ""; a bad finished document and a bad merge record are errors.
func TestSafeHead(t *testing.T) {
	finished := func(head string) string { return `{"schema":"guildhall.finished/1","head":"` + head + `"}` }
	for _, c := range []struct {
		name  string
		files map[string]string
		docs  []string
		want  string
		err   string
	}{
		{name: "merge over finished", files: map[string]string{"deliver-1-merge": prRecord(headA), "finished": finished(headB)}, docs: []string{"deliver-1-merge", "finished"}, want: headA},
		{name: "finished", files: map[string]string{"finished": finished(headB)}, docs: []string{"finished"}, want: headB},
		{name: "finished without head", files: map[string]string{"finished": `{"schema":"guildhall.finished/1"}`}, docs: []string{"finished"}},
		{name: "none", docs: []string{"build-1-prepared"}},
		{name: "finished bad schema", files: map[string]string{"finished": `{"schema":"x","head":"` + headB + `"}`}, docs: []string{"finished"}, err: "document finished is not a guildhall.finished/1 document"},
		{name: "finished invalid head", files: map[string]string{"finished": finished("nope")}, docs: []string{"finished"}, err: "document finished names an invalid head"},
		{name: "finished unreadable", docs: []string{"finished"}, err: "document finished: no such file"},
		{name: "bad merge record", files: map[string]string{"deliver-1-merge": "{", "finished": finished(headB)}, docs: []string{"deliver-1-merge", "finished"}, err: "merge record deliver-1-merge: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := &recordReader{files: c.files}
			got, err := interp.SafeHead(r.refs(c.docs...), r.get)
			if c.err != "" {
				if err == nil || !strings.HasPrefix(err.Error(), c.err) {
					t.Fatalf("SafeHead = %q, %v; want error %q", got, err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("SafeHead = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

// TestParseResultName: <step>-<round>-result with a canonical positive round, the step possibly holding dashes.
func TestParseResultName(t *testing.T) {
	for _, c := range []struct {
		name  string
		step  string
		round int
		ok    bool
	}{
		{"validate-1-result", "validate", 1, true},
		{"ci-check-12-result", "ci-check", 12, true},
		{"validate-0-result", "", 0, false},
		{"validate-01-result", "", 0, false},
		{"validate-1", "", 0, false},
		{"validate-result", "", 0, false},
		{"-1-result", "", 0, false},
		{"validate-1-report", "", 0, false},
	} {
		step, round, ok := interp.ParseResultName(c.name)
		if step != c.step || round != c.round || ok != c.ok {
			t.Errorf("ParseResultName(%q) = %q, %d, %v; want %q, %d, %v", c.name, step, round, ok, c.step, c.round, c.ok)
		}
	}
}
