package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const specPath = "tasks/guildhall/d122/plan-1/spec.md"

// approvalQuestion is a plan approval's question as render.Question writes it, with part after its first line.
func approvalQuestion(part string) string {
	q := "Approval needed for guildhall/d122, step accept (guildmaster-approval version 1), attempt 3.\n"
	if part != "" {
		q += "\n" + part + "\n"
	}
	return q + "\nInputs:\n- spec: " + specPath + " (sha256:abc)\n\nAnswer with one of: approved, changes_requested.\n"
}

// summaryPage renders the fixture task with a plan approval ask about plan-1-spec, whose file holds spec (the
// fixture's when empty) and whose record has its digest unless mismatch, and returns the page.
func summaryPage(t *testing.T, spec, question string, mismatch bool) string {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	if spec != "" {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(specPath)), []byte(spec), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mismatch {
			s := sha256.Sum256([]byte(spec))
			sums[specPath] = hex.EncodeToString(s[:])
		}
	}
	task := fixtureTask(sums)
	task.Asks = append(task.Asks, Ask{ID: "accept-3", Kind: "approval", Question: question, Options: []string{"approved", "changes_requested"},
		Subject: "sha256:" + sums[specPath], SubjectDocument: "plan-1-spec", OpenedAt: at("2026-11-04 14:40")})
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="accept-3"`)
	if i < 0 {
		t.Fatal("the page has no approval ask")
	}
	return body[i:]
}

func once(t *testing.T, page, want string) {
	t.Helper()
	if n := strings.Count(page, want); n != 1 {
		t.Errorf("the ask holds %q %d times, want once", want, n)
	}
}

// TestPlanSummary: a plan approval ask shows its spec's Summary block, rendered, under the question's first line and
// once, whether or not the stored question carries a copy; a spec without one, or whose file does not match its
// digest, shows the fallback link and nothing of the file; a hostile summary is made safe by goldmark.
func TestPlanSummary(t *testing.T) {
	_, block, _ := strings.Cut(fixtureFiles[specPath], "## Summary\n\n")
	block, _, _ = strings.Cut(block, "\n\n## Goal")
	t.Run("from the spec", func(t *testing.T) {
		for name, q := range map[string]string{"question with a copy": approvalQuestion("Plan summary:\n" + block), "older question": approvalQuestion("")} {
			page := summaryPage(t, "", q, false)
			once(t, page, `<div class="plan md"><p class="ph">Plan summary</p>`)
			once(t, page, "Q1 Which address?")
			once(t, page, "<strong>Goal:</strong> one web page per task.")
			once(t, page, "<strong>Changed since the grill:</strong>")
			if strings.Contains(page, "No summary in this spec") || strings.Contains(page, "Plan summary:") {
				t.Errorf("%s: the summary shows twice or with the fallback", name)
			}
			if !strings.Contains(page, "<pre>Inputs:\n- spec: ") {
				t.Errorf("%s: the rest of the question lost its inputs", name)
			}
			if q, p := strings.Index(page, `<p class="q">`), strings.Index(page, `<div class="plan md">`); q < 0 || p < q || strings.Index(page, `class="ctx"`) < p {
				t.Errorf("%s: the summary is not under the question's first line and before the fold", name)
			}
		}
	})
	t.Run("no block", func(t *testing.T) {
		page := summaryPage(t, "# The spec\n\n## Goal\n\nNo summary here.\n", approvalQuestion("No summary in this spec; read "+specPath+"."), false)
		once(t, page, `<a href="/guildhall/d122/doc/plan-1-spec">No summary in this spec · read plan-1-spec</a>`)
		once(t, page, "No summary in this spec")
		ask, _, _ := strings.Cut(page, `class="opts"`)
		once(t, ask, `href="/guildhall/d122/doc/plan-1-spec"`)
		if strings.Contains(page, `class="plan md"`) {
			t.Error("a spec with no block shows a summary")
		}
	})
	t.Run("digest mismatch", func(t *testing.T) {
		page := summaryPage(t, "# Edited\n\n## Summary\n\n- **Goal:** SECRET\n", approvalQuestion(""), true)
		once(t, page, "No summary in this spec · read plan-1-spec")
		if strings.Contains(page, "SECRET") || strings.Contains(page, `class="plan md"`) {
			t.Error("a spec whose file does not match its digest is shown")
		}
	})
	t.Run("hostile", func(t *testing.T) {
		page := summaryPage(t, "# T\n\n## Summary\n\n- **Goal:** <script>alert(1)</script> [bad](javascript:alert(1)) <img src=x onerror=alert(1)>\n", approvalQuestion(""), false)
		once(t, page, `<div class="plan md">`)
		for _, bad := range []string{"<script", "javascript:", "<img"} {
			if strings.Contains(page, bad) {
				t.Errorf("the summary holds %q", bad)
			}
		}
	})
}

// TestDesignRoundNoSummary: a design round's approval ask, bound to its design document, is not a plan approval and
// shows neither a plan summary nor the no-summary fallback; its subject line still links the design.
func TestDesignRoundNoSummary(t *testing.T) {
	dir, sums := writeFixtureFiles(t)
	task := fixtureTask(sums)
	const p = "tasks/guildhall/d122/design-1/design.html"
	task.Asks = append(task.Asks, Ask{ID: "design-1", Kind: "approval", Question: "Design round 1 for guildhall/d122: approve the design?\n\nThe design is design-1-design.",
		Options: []string{"approved", "changes_requested"}, Subject: "sha256:" + sums[p], SubjectDocument: "design-1-design", OpenedAt: at("2026-11-04 14:40")})
	s := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, s, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="design-1"`)
	if i < 0 {
		t.Fatal("the page has no design ask")
	}
	page, _, _ := strings.Cut(body[i:], `class="opts"`)
	for _, bad := range []string{"No summary in this spec", `class="plan md"`, "Plan summary"} {
		if strings.Contains(page, bad) {
			t.Errorf("a design round's ask holds %q", bad)
		}
	}
	once(t, page, `<a href="/guildhall/d122/doc/design-1-design">read design-1-design</a>`)
}

func TestStripSummary(t *testing.T) {
	for _, c := range []struct{ rest, summary, want string }{
		{"Plan summary:\n- a\n\n- b\n\nInputs:\n- x", "- a\n\n- b", "Inputs:\n- x"},
		{"Plan summary:\n- a, as stored\n\nAnswer with one of: y.", "- a", "Answer with one of: y."},
		{"Plan summary:\n- a\n\nInputs:\n- x", "", "Plan summary:\n- a\n\nInputs:\n- x"},
		{"No summary in this spec; read s.md.\n\nInputs:\n- x", "", "Inputs:\n- x"},
		{"Inputs:\n- x", "- a", "Inputs:\n- x"},
	} {
		if got := stripSummary(c.rest, c.summary); got != c.want {
			t.Errorf("stripSummary(%q, %q) = %q, want %q", c.rest, c.summary, got, c.want)
		}
	}
}
