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
