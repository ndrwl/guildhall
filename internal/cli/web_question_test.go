package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/config"
	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/render"
	"github.com/ndrwl/guildhall/internal/spec"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// questionSpec is a plan's spec with a Summary block and an open question, each worded so it can be counted on the
// page.
const questionSpec = `# Name the parts once

## Summary

- Goal: the page names each part of the question once.
- Size: small.

## Goal

The question and the page agree on its parts.

## Open questions

**Q1. Keep the marker wording?**
Options: (a) keep it; (b) reword it.
Recommended: keep the marker wording as it is.
`

// questionPage opens a plan approval on the clerk fixture's a-approve, its question as render.Question writes it for
// a spec holding body (or, with noSummary, a stage with no summary), bound to that spec's digest, and returns the
// task page the cli's source serves for it.
func questionPage(t *testing.T, body string, noSummary bool) string {
	t.Helper()
	dir := clerkFixture(t)
	rel := "tasks/app/a-approve/plan-2/spec.md"
	path := filepath.Join(dir, "state", "documents", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, body)
	sum := digest.Of([]byte(body)).Hex()

	stage := render.Stage{Repository: "app", Task: "a-approve", Attempt: 2,
		Step: workflow.Step{ID: "accept", Type: "guildmaster-approval", Version: 1,
			Settings: map[string]json.RawMessage{"instructions": json.RawMessage(`"Check the scope."`)}}, NoSummary: noSummary}
	if !noSummary {
		stage.Summary, _ = spec.Summary([]byte(body))
	}
	stage.Questions, _ = spec.SpecPart([]byte(body))
	ask, err := render.Question(stage, []render.Input{{Port: "spec", Name: rel, SHA256: sum}})
	if err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(dir, "director.store"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDocument("doc:plan-2-spec", store.DocumentRef{Repository: "app", TaskID: "a-approve", Name: "plan-2-spec", Path: rel, SHA256: sum}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenDecision("open:accept-2", store.Decision{Repository: "app", TaskID: "a-approve", ID: "accept-2", Kind: store.DecisionApproval,
		Subject: &store.Subject{Kind: store.SubjectDocument, Digest: "sha256:" + sum}, Options: ask.Options, Question: ask.Question}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	app, _, _ := newTestApp(nil, nil)
	app.Now = webSourceFor(t, dir).a.Now
	c, err := app.clerkCopy(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := app.webHandler(c, &config.Web{Address: pageHost}, &strings.Builder{})
	r := httptest.NewRequest(http.MethodGet, "/app/a-approve", nil)
	r.Host = pageHost
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /app/a-approve = %d:\n%s", w.Code, w.Body)
	}
	return w.Body.String()
}

// TestWebShowsRenderedQuestionOnce: a plan approval whose question render.Question wrote shows its spec's summary and
// open questions once, from the spec, with no copy of the question's own summary part, and keeps the question's
// Instructions, Inputs and Answer parts; with no summary, the question's no-summary line is gone from its body. The
// page and render share the question's part markers (spec), so rewording one side cannot show the summary twice.
func TestWebShowsRenderedQuestionOnce(t *testing.T) {
	page := questionPage(t, questionSpec, false)
	for _, want := range []string{"the page names each part of the question once", "Keep the marker wording?"} {
		if n := strings.Count(page, want); n != 1 {
			t.Errorf("the page shows %q %d times, want once:\n%s", want, n, page)
		}
	}
	if strings.Contains(page, spec.SummaryHeader) {
		t.Errorf("the page keeps the question's %q copy:\n%s", spec.SummaryHeader, page)
	}
	for _, want := range []string{spec.InstructionsHeader, "Check the scope.", spec.InputsHeader, spec.AnswerLine} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lost the question's %q part:\n%s", want, page)
		}
	}

	page = questionPage(t, strings.Replace(questionSpec, "## Summary", "## Overview", 1), true)
	if strings.Contains(page, spec.NoSummaryLine+"; read") || strings.Count(page, spec.NoSummaryLine) != 1 {
		t.Errorf("the page keeps the question's %q line, or lost its own:\n%s", spec.NoSummaryLine, page)
	}
	if n := strings.Count(page, "Keep the marker wording?"); n != 1 {
		t.Errorf("the page shows the open question %d times, want once:\n%s", n, page)
	}
	for _, want := range []string{spec.InputsHeader, spec.AnswerLine} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lost the question's %q part:\n%s", want, page)
		}
	}
}
