package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/spec"
)

const reportPath = "tasks/guildhall/d122/hallmark-1/report.md"

// followUpsQuestion is a follow-ups question as the engine's followUpsQuestion writes it, with part after its ranked
// list.
func followUpsQuestion(part string) string {
	q := "Follow-ups of hallmark task guildhall/d122.\nReport: /state/" + reportPath + "\n\nRanked follow-ups:\nH1. Tighten the parser\n"
	if part != "" {
		q += "\n" + part + "\n"
	}
	return q + "\nAnswer pick, then the ids of the follow-ups to make tasks of on the following lines, one per line or comma-separated, for example:\n" +
		"pick\nH1, H3\nor answer none to make no task. Guildhall creates no task; the Director creates one for each id picked.\n"
}

// followUpsPage renders the fixture task with a follow-ups ask carrying question, bound to a hallmark report holding
// report whose record has that file's digest unless mismatch, and returns the ask up to its options.
func followUpsPage(t *testing.T, report, question string, mismatch bool) string {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	full := filepath.Join(dir, filepath.FromSlash(reportPath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256([]byte(report))
	sum := hex.EncodeToString(s[:])
	recorded := sum
	if mismatch {
		recorded = strings.Repeat("0", 64)
	}
	task := fixtureTask(sums)
	task.Documents = append(task.Documents, Document{Repository: "guildhall", TaskID: "d122", Name: "hallmark-1-report", Path: reportPath, SHA256: recorded,
		Kind: "Hallmark", Step: "hallmark", Attempt: 1, RecordedAt: at("2026-11-04 14:38")})
	task.Asks = append(task.Asks, Ask{ID: "follow-ups", Kind: "follow-ups", Question: question, Options: []string{"pick", "none"},
		Subject: "sha256:" + sum, SubjectDocument: "hallmark-1-report", OpenedAt: at("2026-11-04 14:38")})
	srv := New(Options{Source: &fakeSource{tasks: map[string]Task{"guildhall/d122": task}}, Documents: dir, Location: time.UTC, Host: testHost})
	w := get(t, srv, "/guildhall/d122")
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	i := strings.Index(body, `id="follow-ups"`)
	if i < 0 {
		t.Fatal("the page has no follow-ups ask")
	}
	ask, _, _ := strings.Cut(body[i:], `<p class="who">`)
	return ask
}

// TestReportQuestions: a follow-ups ask shows its hallmark report's questions as cards, the recommended answer first
// and marked and the alternative after it, once, in a box of its own, and the rest of the question keeps its ranked
// list and answer format but not the questions, in today's shape or an older ask's; its pick and none keep their
// tiles; a report whose section says None says so; an older report with no section, or one whose file does not
// verify, shows no box and keeps its question whole; hostile questions are made safe.
func TestReportQuestions(t *testing.T) {
	const qs = "**Q1. Change direction**\nShould the next slice move on?\nRecommended: yes, because the relay is done.\nAlternative: finish the CLI first, a week later."
	report := "# Hallmark\n\n## Fixes\n\n### H1. Tighten the parser\n\n## Questions\n\n" + qs + "\n\n## Procedure\n\n- none.\n"
	part, ok := spec.ReportPart([]byte(report), spec.QuestionsMax)
	if !ok {
		t.Fatal("the report's questions do not read")
	}
	oldPart := "Open questions in full:\n" + qs
	t.Run("cards", func(t *testing.T) {
		for name, q := range map[string]string{"question with a copy": followUpsQuestion(part), "stored copy that differs": followUpsQuestion(part + " As stored."),
			"cut copy": followUpsQuestion("**Open questions · 1**\n\n(cut; read the spec)"), "question with an old copy": followUpsQuestion(oldPart),
			"old cut copy": followUpsQuestion("Open questions in full:\n(cut; read the spec)"), "older question": followUpsQuestion("")} {
			page := followUpsPage(t, report, q, false)
			once(t, page, `<div class="plan md"><p class="ph oqh">Open questions <span class="n">1</span></p><div class="oqs">
<section class="oq"><p class="qt"><span class="qn">Q1</span>Change direction</p><div class="qb"><p>Should the next slice move on?</p>
</div><ul class="ol"><li class="rec"><span class="ot">yes, because the relay is done.</span><i class="badge">Recommended</i></li><li><span class="lt">Alternative</span><span class="ot">finish the CLI first, a week later.</span></li></ul></section></div></div>`)
			box, rest, _ := strings.Cut(page, `class="ctx"`)
			if strings.Contains(box, `<p class="ph">Summary</p>`) || strings.Contains(box, "No summary in this spec") {
				t.Errorf("%s: a follow-ups ask shows a plan summary", name)
			}
			if strings.Contains(rest, "Change direction") || strings.Contains(rest, "Open questions") || strings.Contains(rest, "(cut;") {
				t.Errorf("%s: the rest of the question repeats the questions:\n%s", name, rest)
			}
			if !strings.Contains(rest, "Ranked follow-ups:\nH1. Tighten the parser\n\nAnswer pick, then the ids") {
				t.Errorf("%s: the rest of the question lost its ranked list or answer format:\n%s", name, rest)
			}
			if q, p := strings.Index(page, `<p class="q">`), strings.Index(page, `<div class="plan md"><p class="ph oqh">Open questions`); q < 0 || p < q || strings.Index(page, `class="ctx"`) < p {
				t.Errorf("%s: the questions are not under the question's first line and before the fold", name)
			}
			once(t, page, `<a href="/guildhall/d122/doc/hallmark-1-report">read hallmark-1-report</a>`)
			if strings.Contains(page, "take every recommendation") || strings.Contains(page, `opt pos rec`) {
				t.Errorf("%s: a follow-ups ask offers a recommended answer", name)
			}
		}
	})
	t.Run("none", func(t *testing.T) {
		page := followUpsPage(t, "# Hallmark\n\n## Questions\n\nNone.\n", followUpsQuestion("Open questions: none."), false)
		once(t, page, `<div class="plan md"><p class="ph">Open questions: none.</p></div>`)
		once(t, page, "Open questions: none.")
	})
	for name, c := range map[string]struct {
		report   string
		mismatch bool
	}{"older report": {"# Hallmark\n\n### H1. Tighten the parser\n", false}, "digest mismatch": {report, true}} {
		t.Run(name, func(t *testing.T) {
			page := followUpsPage(t, c.report, followUpsQuestion(""), c.mismatch)
			if strings.Contains(page, "Open questions") || strings.Contains(page, "Change direction") {
				t.Errorf("the ask shows open questions:\n%s", page)
			}
			if !strings.Contains(page, "Ranked follow-ups:\nH1. Tighten the parser\n\nAnswer pick") {
				t.Error("the question is not kept whole")
			}
		})
	}
	t.Run("hostile", func(t *testing.T) {
		bad := "# H\n\n## Questions\n\n**Q1. <script>alert(1)</script>**\nRecommended: [x](javascript:alert(1)).\nAlternative: <img src=x onerror=alert(1)>.\n"
		page := followUpsPage(t, bad, followUpsQuestion(""), false)
		once(t, page, `<p class="ph oqh">Open questions <span class="n">1</span></p>`)
		once(t, page, `<li class="rec"><span class="ot"><a href="">x</a>.</span>`)
		for _, b := range []string{"<script", "javascript:", "<img"} {
			if strings.Contains(withoutOwnScripts(page), b) {
				t.Errorf("the questions hold %q", b)
			}
		}
	})
}

// TestStripReportQuestions: the follow-ups question's open-questions part, in full, none or cut, comes out from its
// heading up to the answer format; a question without one is unchanged.
func TestStripReportQuestions(t *testing.T) {
	const list, answer = "Report: r.md\n\nRanked follow-ups:\nH1. x", "\n\nAnswer pick, then the ids."
	for _, c := range []struct{ rest, want string }{
		{list + "\n\nOpen questions in full:\n**Q1. a**\nRecommended: b." + answer, list + answer},
		{list + "\n\nOpen questions: none." + answer, list + answer},
		{"Report: r.md\n\nOpen questions in full:\n(cut; read the spec)" + answer, "Report: r.md" + answer},
		{list + answer, list + answer},
		{list + "\n\nOpen questions in full:\n**Q1. a**", list},
		{list + "\n\n**Open questions · 1**\n\n**Q1. a**\n- ★ **Recommended:** b.\n- Alternative: c." + answer, list + answer},
		{"Report: r.md\n\n**Open questions · 3**\n\n(cut; read the spec)" + answer, "Report: r.md" + answer},
		{list + "\n\n**Open questions · 1**\n\n**Q1. a**", list},
	} {
		if got := stripReportQuestions(c.rest); got != c.want {
			t.Errorf("stripReportQuestions(%q) = %q, want %q", c.rest, got, c.want)
		}
	}
}
