package doccheck

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate the decision index, docs/decisions.md")

// repoRoot is the repository root, seen from this package's directory.
const repoRoot = "../.."

// moved lists former records that are no longer records, with where their content now lives, as a link from docs/.
// A record's former names live in its own formerly header; this list is only for files whose content left the library.
var moved = map[string]movedFile{
	"forge-gh-command-syntax.md":            {Target: "contracts/forge.md#command-syntax-audit", Label: "forge contract: command syntax audit"},
	"launcher-stop-reply-drain.md":          {Target: "contracts/endpoint.md#shutdown-drain", Label: "endpoint contract: shutdown drain"},
	"preflight-lock-wait-test-budget.md":    {Target: "test-timing.md#preflight-lock-wait", Label: "test timing: preflight lock wait"},
	"workflow-model-and-encoding-review.md": {Target: "evidence/workflow-model-and-encoding-review-2026-10-30.md", Label: "workflow model and encoding review (evidence)"},
}

func fail(t *testing.T, what string, problems []string) {
	t.Helper()
	if len(problems) > 0 {
		t.Errorf("%s: %d problems:\n%s", what, len(problems), strings.Join(problems, "\n"))
	}
}

// TestDecisionLibrary checks every record's header, name and sections, the supersede and amend pairs, and the former names.
func TestDecisionLibrary(t *testing.T) {
	recs, problems := loadLibrary(filepath.Join(repoRoot, "docs", libDir))
	fail(t, "records", problems)
	fail(t, "relations", checkRelations(recs))
	fail(t, "former names", checkFormerly(recs, moved))
	fail(t, "numbers", checkNumbers(recs))
	var missing []string
	for name, m := range moved {
		target, _, _ := strings.Cut(m.Target, "#")
		if _, err := os.Stat(filepath.Join(repoRoot, "docs", filepath.FromSlash(target))); err != nil {
			missing = append(missing, name+": moved to "+m.Target+", which does not exist")
		}
	}
	fail(t, "moved files", missing)
}

// TestDecisionIndex checks that the committed index equals the one generated from the records' headers.
// Run it with -update to regenerate the index.
func TestDecisionIndex(t *testing.T) {
	_, problems := checkIndex(repoRoot, moved, *update)
	fail(t, "index", problems)
}

// TestDecisionLinks checks that every path and relative link into the library, in Go and Markdown, names an existing record.
func TestDecisionLinks(t *testing.T) {
	fail(t, "links", checkLinks(repoRoot))
}

// reversedClaims are sentences the Steward message decision (docs/decisions/steward-messages-directors.md) reversed,
// which no page or comment may still state. The decision library itself keeps its history.
var reversedClaims = []string{"never messages a director", "steward never messages", "never tells the director by message"}

// TestNoReversedClaims checks that no Markdown page outside the decision library, and no Go file, still says the
// Steward never messages a Director.
func TestNoReversedClaims(t *testing.T) {
	var problems []string
	for _, dir := range []string{"docs", "internal", "skills", "baseline", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			skip := filepath.Base(filepath.Dir(path)) == libDir || filepath.Base(path) == "tree_test.go"
			if skip || !strings.HasSuffix(path, ".md") && !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := strings.ToLower(strings.Join(strings.Fields(string(data)), " "))
			for _, claim := range reversedClaims {
				if strings.Contains(text, claim) {
					problems = append(problems, path+": says "+claim)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fail(t, "reversed claims", problems)
}
