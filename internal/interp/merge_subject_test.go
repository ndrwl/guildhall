package interp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/forge/forgetest"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/workflow"
)

// legacyPut records pull request document name as Guildhall wrote it before titles were recorded: the same fields,
// without "title". The advance that recorded it still holds the reference it meant to record, so a read of the
// document later in that advance fails its digest check, as a crash would end it; advanceLegacy retries it, and the
// replay reads the legacy record from the store.
type legacyPut struct {
	interp.Records
	dir, name string
	done      bool
}

func (l *legacyPut) PutDocument(m string, d store.DocumentRef) (store.Receipt, error) {
	if d.Name != l.name || l.done {
		return l.Records.PutDocument(m, d)
	}
	l.done = true
	file := filepath.Join(l.dir, filepath.FromSlash(d.Path))
	data, err := os.ReadFile(file)
	if err != nil {
		return store.Receipt{}, err
	}
	var old struct {
		Schema string `json:"schema"`
		Remote string `json:"remote"`
		forge.PullRequest
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return store.Receipt{}, err
	}
	if data, err = json.Marshal(old); err != nil {
		return store.Receipt{}, err
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		return store.Receipt{}, err
	}
	d.SHA256 = digest.Of(data).Hex()
	return l.Records.PutDocument(m, d)
}

// advanceLegacy is advance, retried once when legacy is set and the advance that recorded a legacy document fails.
func (e *env) advanceLegacy(t *testing.T, legacy bool, ev interp.Event) interp.Next {
	t.Helper()
	if legacy {
		ev.Repository, ev.Task = "app", "t7"
		next, err := e.eng.Advance(context.Background(), ev)
		if err == nil {
			return next
		}
		t.Logf("Advance(%+v) after recording a legacy document = %v; replaying it", ev, err)
	}
	return e.advance(t, ev)
}

// forgeTitle is the title pull request number of the fake forge holds now.
func forgeTitle(t *testing.T, f *forgetest.Fake, number int) string {
	t.Helper()
	title, _ := f.Text(repoRemote, number)
	if title == "" {
		t.Fatalf("pull request %d has no title", number)
	}
	return title
}

// TestMergeSubjectIsThePublishedTitle: publish records the title it published on <step>-pr, and the merge's subject
// is that title plus (#N); a <step>-pr recorded before titles were gets its title recomputed as its publish made it,
// so it merges with the same subject.
func TestMergeSubjectIsThePublishedTitle(t *testing.T) {
	for _, b := range backends {
		for _, legacy := range []bool{false, true} {
			t.Run(b.name+map[bool]string{false: "/recorded", true: "/legacy"}[legacy], func(t *testing.T) {
				e, f := newPublishEnv(t, b)
				next := interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"}
				if legacy {
					e.eng.Records = &legacyPut{Records: e.eng.Records, dir: e.eng.DocumentDir, name: "publish-1-pr"}
					_ = e.toSignOffErr(t) // the advance that recorded the legacy document, replayed next
					want(t, e.advanceLegacy(t, legacy, interp.Event{Kind: interp.DecisionAnswered, Decision: "sign-off-1"}), next)
				} else {
					want(t, e.toSignOff(t), next)
				}
				title := forgeTitle(t, f, 1)
				pr := e.pullRequestDoc(t, "publish-1-pr")
				if got, ok := pr["title"]; legacy && ok || !legacy && got != title {
					t.Fatalf("publish-1-pr title = %v, want %q", got, map[bool]string{false: title, true: "none"}[legacy])
				}
				e.answer(t, "deliver-1", "merge")
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
				if got, want := f.MergeSubject(repoRemote, 1), title+" (#1)"; got != want {
					t.Errorf("merge subject = %q, want %q", got, want)
				}
				if _, ok := e.pullRequestDoc(t, "deliver-1-merge")["title"]; ok {
					t.Error("deliver-1-merge records a title")
				}
			})
		}
	}
}

// TestMergeSubjectAfterARebase: a head validate rebased is merged with the title the rebase link pushed, recorded on
// <link>-pr, not the publish step's; a <link>-pr recorded before titles were gets the link title.
func TestMergeSubjectAfterARebase(t *testing.T) {
	for _, b := range backends {
		for _, legacy := range []bool{false, true} {
			t.Run(b.name+map[bool]string{false: "/recorded", true: "/legacy"}[legacy], func(t *testing.T) {
				// The publish step takes the accepted spec, so its title is the spec's and the link's is the task's.
				def := loadWorkflow(t, "validate")
				for i := range def.Steps {
					if def.Steps[i].ID == "publish" {
						def.Steps[i].Inputs["spec"] = workflow.Refs{"accept.spec"}
					}
				}
				e, f, ws := validateRebaseEnvDef(t, b, def, nil)
				if legacy {
					e.eng.Records = &legacyPut{Records: e.eng.Records, dir: e.eng.DocumentDir, name: "validate-1-rebase-1-pr"}
				}
				clean(ws, baseN(1), head, head2)
				want(t, e.advanceLegacy(t, legacy, reviewed), waitChecks)
				published := e.pullRequestDoc(t, "publish-1-pr")["title"]
				title := forgeTitle(t, f, 1)
				if title == published {
					t.Fatalf("the link pushed the publish step's title %q; the test cannot tell them apart", title)
				}
				if got, ok := e.pullRequestDoc(t, "validate-1-rebase-1-pr")["title"]; legacy && ok || !legacy && got != title {
					t.Fatalf("validate-1-rebase-1-pr title = %v, want %q", got, title)
				}
				f.SetChecks(repoRemote, head2, ciPassed...)
				f.SetMergeability(forge.Mergeability{})
				want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
				e.answer(t, "deliver-1", "merge")
				want(t, e.advance(t, interp.Event{Kind: interp.DecisionAnswered, Decision: "deliver-1"}), done)
				if mergedHead(f) != head2 {
					t.Fatal("not merged at the rebased head")
				}
				if got, want := f.MergeSubject(repoRemote, 1), title+" (#1)"; got != want {
					t.Errorf("merge subject = %q, want %q", got, want)
				}
			})
		}
	}
}
