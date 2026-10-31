package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeSource is a Source holding fixed tasks, or failing with err.
type fakeSource struct {
	tasks map[string]Task
	err   error
	calls int
}

func (f *fakeSource) Task(repo, id string) (Task, error) {
	f.calls++
	if f.err != nil {
		return Task{}, f.err
	}
	t, ok := f.tasks[repo+"/"+id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func at(s string) *time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return &t
}

// fixtureFiles are the document files the fixture records, by path under the document directory.
var fixtureFiles = map[string]string{
	"tasks/guildhall/d122/grill-3/notes.md":      "# d122 grill-3 notes\n\n## Decisions\n- **Server:** the Director session's supervisor serves the page.\n- **Access:** tailnet only.\n- **Rendering:** goldmark with `tables`.\n\n## Facts\n\n- not a decision\n",
	"tasks/guildhall/d122/grill-2/notes.md":      "# d122 grill-2 notes\n\n## Decisions\n- older\n",
	"tasks/guildhall/d122/grill-1/notes.md":      "# d122 grill-1 notes\n\n## Decisions\n- oldest\n",
	"tasks/guildhall/d122/plan-1/spec.md":        "# The spec\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))\n",
	"tasks/guildhall/d122/review-1/outcome.json": `{"verdict":"changes_requested"}`,
	"tasks/guildhall/d122/review-2/outcome.json": `{"verdict":"changes_requested"}`,
	"tasks/guildhall/d122/design-1/design.html":  "<!doctype html><p>a design</p><script>alert(1)</script>",
	"tasks/guildhall/d105/design-3/design.md":    "# d105 design-3\n",
}

// writeFixtureFiles writes fixtureFiles under a new document directory and returns it with each file's sha256.
func writeFixtureFiles(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	sums := map[string]string{}
	for p, body := range fixtureFiles {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		s := sha256.Sum256([]byte(body))
		sums[p] = hex.EncodeToString(s[:])
	}
	return dir, sums
}

// fixtureTask is d122 as design-3's mock-up draws a task: two open asks (a review loop exit and a grant), a deferred
// merge, three grill rounds, a review sent back to build, a void superseded acceptance, and two slices with slice 1
// merged, while the Director also runs a grill stage with the Guildmaster.
func fixtureTask(sums map[string]string) Task {
	doc := func(name, p, kind, step string, round int, when string) Document {
		return Document{Repository: "guildhall", TaskID: "d122", Name: name, Path: p, SHA256: sums[p], Kind: kind, Step: step, Attempt: round, RecordedAt: at(when)}
	}
	return Task{
		Repository: "guildhall", ID: "d122",
		Title:  "Guildmaster task page: one web page per task with context, status and answerable decisions",
		State:  "open",
		Stands: "waiting for your answer",
		Related: []Related{
			{Label: "linked from", Repository: "guildhall", ID: "d105"},
			{Label: "depends on", Repository: "other", ID: "d7"},
		},
		Steps: []Step{
			{Name: "Grill", State: StateDone, Note: "3 rounds"}, {Name: "Research", State: StateDone, Note: "1 round"},
			{Name: "Plan", State: StateDone, Note: "1 round"}, {Name: "Accept", State: StateDone, Note: "accept-2 approved"},
			{Name: "Build", State: StateDone, Note: "2 rounds"}, {Name: "Review", State: StateYou, Note: "waiting on you"},
			{Name: "Publish", State: StateLater, Note: "later"}, {Name: "Deliver", State: StateLater, Note: "later"},
		},
		Slices: &Slices{Current: 2, Total: 2, Items: []Slice{
			{Index: 1, Title: "Serve read-only task and reader pages on the tailnet", State: "merged", PR: &PullRequest{Number: 247, URL: "https://github.com/ndrwl/guildhall/pull/247", Merged: true}},
			{Index: 2, Title: "Answer decisions on the task page", State: "current", PR: &PullRequest{Number: 251, URL: "https://github.com/ndrwl/guildhall/pull/251"}},
		}},
		Status: Status{Step: "Review", Round: 2, Attempt: &Attempt{Step: "review--s2", Number: 2, Outcome: "succeeded", At: at("2026-11-04 14:20")},
			PR: &PullRequest{Number: 251, URL: "https://github.com/ndrwl/guildhall/pull/251"}, CI: CIPassed},
		Asks: []Ask{
			{ID: "review--s2-x2", Kind: "loop-exit", Question: "Review round 2 still asks for changes. Continue, accept or reject?\n\nThe review is review--s2-2-outcome.",
				Options: []string{"continue", "accept", "reject"}, Subject: "3f9c2ab0c0ffee0000000000000000000000beef", OpenedAt: at("2026-11-04 14:20"),
				Findings: []Finding{{ID: "F4", Rule: "medium", Summary: "a sha256 mismatch returns a 500 with the state path"}}},
			{ID: "grant-1", Kind: "grant", Question: "Grant network to build-3, so it can run the real tailscale whois contract test?",
				Options: []string{"approve", "reject"}, OpenedAt: at("2026-11-04 14:31")},
			{ID: "deliver-1", Kind: "merge", Question: "Merge d122?", Options: []string{"merge", "hold", "later"}, Deferred: true, OpenedAt: at("2026-11-04 14:35")},
		},
		DirectorStage: "grill",
		DirectorWaits: []string{"checks of PR #251 are pending"},
		Context: []Quote{
			{Text: "Perhaps its better if we move things like grills and decisions for me into a web interface", Author: "director", At: *at("2026-10-31 09:12")},
			{Text: "Ideally a page might have a full summary of the task", Author: "guildmaster", At: *at("2026-10-31 09:12")},
		},
		Documents: []Document{
			doc("grill-1-notes", "tasks/guildhall/d122/grill-1/notes.md", "Grill", "grill", 1, "2026-10-31 09:40"),
			doc("grill-2-notes", "tasks/guildhall/d122/grill-2/notes.md", "Grill", "grill", 2, "2026-10-31 11:50"),
			doc("grill-3-notes", "tasks/guildhall/d122/grill-3/notes.md", "Grill", "grill", 3, "2026-10-31 15:40"),
			doc("plan-1-spec", "tasks/guildhall/d122/plan-1/spec.md", "Spec", "plan", 1, "2026-10-31 16:10"),
			doc("review-1-outcome", "tasks/guildhall/d122/review-1/outcome.json", "Review", "review", 1, "2026-11-04 11:40"),
			doc("review--s2-2-outcome", "tasks/guildhall/d122/review-2/outcome.json", "Review", "review", 2, "2026-11-04 14:20"),
			doc("design-1-design", "tasks/guildhall/d122/design-1/design.html", "Design", "design", 1, "2026-10-31 13:00"),
			{Repository: "guildhall", TaskID: "d105", Name: "design-3-design", Path: "tasks/guildhall/d105/design-3/design.md", SHA256: sums["tasks/guildhall/d105/design-3/design.md"], Kind: "Linked", Step: "design", Attempt: 3, RecordedAt: at("2026-10-31 14:26")},
		},
		Story: []Node{
			{Title: "Task added", State: StateDone, Word: "finished", Start: at("2026-10-31 09:12"), Body: "with your words, shown under “The task”"},
			{Title: "Grill · round 1", State: StateDone, Word: "finished", Start: at("2026-10-31 09:20"), End: at("2026-10-31 09:40"), Documents: []string{"grill-1-notes"}},
			{Title: "Grill · round 2", State: StateDone, Word: "finished", Start: at("2026-10-31 11:00"), End: at("2026-10-31 11:50"), Documents: []string{"grill-2-notes"}},
			{Title: "Grill · round 3", State: StateDone, Word: "finished", Start: at("2026-10-31 15:00"), End: at("2026-10-31 15:40"), Documents: []string{"grill-3-notes"}},
			{Title: "Approval · accept-1", State: StateVoid, Word: "void", Start: at("2026-10-31 16:15"), Body: "void: superseded: the spec changed"},
			{Title: "Approval · accept-2", State: StateDone, Word: "Approved", Start: at("2026-10-31 17:00"), End: at("2026-10-31 17:05"),
				Said: []Said{{Text: "approve all", Where: "answered via the Director or decide answer"}}, Documents: []string{"plan-1-spec"}},
			{Title: "Review · round 1", State: StateBack, Word: "changes requested · sent back to build", Start: at("2026-11-04 10:02"), End: at("2026-11-04 11:40"),
				Findings: []Finding{{ID: "F1", Rule: "high", Summary: "an answer with no Origin is accepted", Fixed: true}}, Documents: []string{"review-1-outcome"}},
			{Title: "Slice 1 merged", State: StateDone, Word: "merged", Start: at("2026-11-04 12:00")},
			{Title: "Build · round 1 · slice 2", State: StateDone, Word: "finished", Start: at("2026-11-04 12:10"), End: at("2026-11-04 13:52")},
			{Title: "Review · round 2 · slice 2", State: StateYou, Word: "changes requested · waiting on you", Start: at("2026-11-04 13:58"), End: at("2026-11-04 14:20"), Documents: []string{"review--s2-2-outcome"}, Ask: "review--s2-x2"},
			{Title: "Grant · grant-1", State: StateYou, Word: "waiting on you", Start: at("2026-11-04 14:31"), Ask: "grant-1"},
		},
		ReadAt: *at("2026-11-04 14:52"),
	}
}

// fixtureServer is a Server over the fixture, with its documents written, showing times in UTC.
func fixtureServer(t *testing.T) (*Server, *fakeSource, string, map[string]string) {
	t.Helper()
	dir, sums := writeFixtureFiles(t)
	src := &fakeSource{tasks: map[string]Task{"guildhall/d122": fixtureTask(sums)}}
	return New(Options{Source: src, Documents: dir, Location: time.UTC, Host: testHost}), src, dir, sums
}
