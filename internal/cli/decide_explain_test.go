package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

// TestDecideExplain: explain records the Director's explanation under revision 1, with its flags after or before the
// arguments; repeating it writes nothing; explaining again replaces it under revision 2; show carries it; and the
// mutation ids carry the revision read.
func TestDecideExplain(t *testing.T) {
	path := decideStorePath(t)
	plan := "sha256:" + strings.Repeat("c", 64)
	code, out, errOut := runDecide(t, "", "explain", "app/t-1", "approve-plan", "--store", path, "--subject-digest", plan, "--text", "The plan **builds** it.\r\n", "--recommend", "approve")
	e := decodeJSON[store.DecisionExplanation](t, out)
	if code != 0 || e.Text != "The plan **builds** it.\n" || e.SubjectDigest != plan || e.Recommend != "approve" || e.Revision != 1 || e.At.IsZero() || e.Decision != "approve-plan" {
		t.Fatalf("explain = %d %+v %s", code, e, errOut)
	}
	before := lastSequence(t, path)
	code, out, _ = runDecide(t, "", "explain", "--store", path, "--subject-digest", plan, "--text", "The plan **builds** it.\n", "--recommend", "approve", "app/t-1", "approve-plan")
	if again := decodeJSON[store.DecisionExplanation](t, out); code != 0 || again != e || lastSequence(t, path) != before {
		t.Errorf("identical explain = %d %+v; want %+v writing nothing", code, again, e)
	}
	file := filepath.Join(t.TempDir(), "explanation.md")
	if err := os.WriteFile(file, []byte("\xef\xbb\xbfSecond words.\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runDecide(t, "", "explain", "app/t-1", "approve-plan", "--store", path, "--subject-digest", plan, "--file", file)
	if e2 := decodeJSON[store.DecisionExplanation](t, out); code != 0 || e2.Text != "Second words.\n" || e2.Recommend != "" || e2.Revision != 2 {
		t.Fatalf("explain again = %d %+v %s", code, e2, errOut)
	}
	code, out, errOut = runDecide(t, "Third, from stdin.", "explain", "app/t-1", "approve-plan", "--store", path, "--subject-digest", plan, "--file", "-")
	if e3 := decodeJSON[store.DecisionExplanation](t, out); code != 0 || e3.Text != "Third, from stdin." || e3.Revision != 3 {
		t.Fatalf("explain from stdin = %d %+v %s", code, e3, errOut)
	}
	if _, err := withStore(path, true, func(s *store.Store) (store.Receipt, error) {
		return s.Receipt("decision-explain:app:t-1:approve-plan:2")
	}); err != nil {
		t.Errorf("the third explanation's receipt: %v", err)
	}
	code, out, _ = runDecide(t, "", "show", "--store", path, "--repo", "app", "--task", "t-1", "--decision", "approve-plan")
	var shown struct {
		ID          string                     `json:"id"`
		Explanation *store.DecisionExplanation `json:"explanation"`
	}
	if err := json.Unmarshal([]byte(out), &shown); code != 0 || err != nil || shown.ID != "approve-plan" || shown.Explanation == nil || shown.Explanation.Revision != 3 {
		t.Errorf("show = %d %s, %v", code, out, err)
	}
	if code, out, _ = runDecide(t, "", "show", "--store", path, "--repo", "lib", "--task", "t-1", "--decision", "port"); code != 0 || strings.Contains(out, "explanation") {
		t.Errorf("show of an unexplained decision = %d %s; want no explanation field", code, out)
	}
}

// TestDecideExplainRefusals: an answered or unknown decision, an unoffered recommendation and blank text are refused
// with exit 1 writing nothing; missing or doubled inputs and bad ids are usage errors.
func TestDecideExplainRefusals(t *testing.T) {
	path := decideStorePath(t)
	d := "sha256:" + strings.Repeat("c", 64)
	before := lastSequence(t, path)
	for name, tc := range map[string]struct {
		args []string
		code int
		err  string
	}{
		"answered":               {[]string{"app/t-1", "done-already", "--subject-digest", d, "--text", "x"}, 1, "is answered"},
		"unknown":                {[]string{"app/t-1", "missing", "--subject-digest", d, "--text", "x"}, 1, "not found"},
		"unoffered":              {[]string{"app/t-1", "approve-plan", "--subject-digest", d, "--text", "x", "--recommend", "approved"}, 1, `"approved" is not one of them`},
		"blank":                  {[]string{"app/t-1", "approve-plan", "--subject-digest", d, "--text", " "}, 1, "must not be empty"},
		"no text":                {[]string{"app/t-1", "approve-plan", "--subject-digest", d}, 2, "one of --text or --file"},
		"text and file":          {[]string{"app/t-1", "approve-plan", "--subject-digest", d, "--text", "x", "--file", "-"}, 2, "one of --text or --file"},
		"no digest":              {[]string{"app/t-1", "approve-plan", "--text", "x"}, 2, "requires --subject-digest"},
		"no decision":            {[]string{"app/t-1", "--subject-digest", d, "--text", "x"}, 2, "takes <repo>/<task> and <decision>"},
		"a task with no repo":    {[]string{"t-1", "approve-plan", "--subject-digest", d, "--text", "x"}, 2, "must be <repo>/<task>"},
		"a decision that is bad": {[]string{"app/t-1", "Bad", "--subject-digest", d, "--text", "x"}, 2, "must be an id"},
	} {
		code, out, errOut := runDecide(t, "", append([]string{"explain", "--store", path}, tc.args...)...)
		if code != tc.code || out != "" || !strings.Contains(errOut, tc.err) {
			t.Errorf("%s: exit %d, %q, stderr %s; want %d with %q", name, code, out, errOut, tc.code, tc.err)
		}
	}
	if lastSequence(t, path) != before {
		t.Error("a refused explanation wrote to the store")
	}
}
