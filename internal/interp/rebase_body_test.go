package interp_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/store"
)

// bodyForge is a forge that records the body of each change published through it.
type bodyForge struct {
	readingForge
	mu     sync.Mutex
	bodies []string
}

func (f *bodyForge) Publish(ctx context.Context, c forge.Change) (forge.PullRequest, error) {
	f.mu.Lock()
	f.bodies = append(f.bodies, c.Body)
	f.mu.Unlock()
	return f.readingForge.Publish(ctx, c)
}

func (f *bodyForge) published() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies)
}

// rebasedLine is the line a rebase push adds to the Guildhall section of publish's body.
func rebasedLine(step, onto, rebased string) string {
	return fmt.Sprintf("- Rebased: by step `%s` onto `%s` as head `%s` (no_conflicts)\n", step, onto, rebased)
}

// withRebased is publish's body with line added after its - Head: line for head h.
func withRebased(t *testing.T, published, h, line string) string {
	t.Helper()
	at := "- Head: `" + h + "`\n"
	if strings.Count(published, at) != 1 {
		t.Fatalf("publish's body has no single %q line:\n%s", at, published)
	}
	return strings.Replace(published, at, at+line, 1)
}

// TestRebasePushKeepsThePublishedBody: a validate rebase pushed to the task's pull request keeps the body publish
// wrote, with its Summary, What changed, How it was tested, Review and Guildhall sections, and adds one - Rebased:
// line naming the validate step, the base and the rebased head; the - Head: line still names the published head, and
// a task that fixes another keeps its fix line once, last.
func TestRebasePushKeepsThePublishedBody(t *testing.T) {
	for _, b := range backends {
		for _, fixes := range []bool{false, true} {
			t.Run(b.name+map[bool]string{false: "", true: "/fixes"}[fixes], func(t *testing.T) {
				var edit func(*store.Task)
				if fixes {
					edit = func(task *store.Task) { task.Fixes = &store.TaskRef{Repository: "app", ID: "t3"} }
				}
				e, f, ws := validateRebaseEnvDef(t, b, loadWorkflow(t, "validate"), edit)
				bf := &bodyForge{readingForge: e.eng.Forge.(readingForge)}
				e.eng.Forge = bf
				clean(ws, baseN(1), head, head2)
				want(t, e.advance(t, reviewed), waitChecks)
				bodies := bf.published()
				if len(bodies) != 2 {
					t.Fatalf("publishes = %d, want publish's and the rebase's", len(bodies))
				}
				_, body := f.Text(repoRemote, 1)
				for _, s := range []string{"## Summary", "## What changed", "## How it was tested", "## Review", "## Guildhall"} {
					if !strings.Contains(body, s+"\n") {
						t.Errorf("rebased body lacks %q:\n%s", s, body)
					}
				}
				if got, want := body, withRebased(t, bodies[0], head, rebasedLine("validate", baseN(1), head2)); got != want {
					t.Fatalf("rebased body:\n%s\nwant publish's body plus the rebase line:\n%s", got, want)
				}
				if fixes {
					const line = "\nFixes a defect introduced by task `app/t3`.\n"
					if strings.Count(body, "Fixes a defect introduced by task") != 1 || !strings.HasSuffix(body, line) {
						t.Errorf("rebased body does not end with the one fix line:\n%s", body)
					}
				}
			})
		}
	}
}

// TestQueueRebasePushKeepsThePublishedBody: the merge-queue front's rebase push keeps publish's body and adds a
// - Rebased: line naming the deliver step; a chain of rebases restates the body with only the latest link's line.
func TestQueueRebasePushKeepsThePublishedBody(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f, ws := queueEnv(t, b)
			_, published := f.Text(repoRemote, 1)
			from := head
			e.answer(t, "deliver-1", "merge")
			ev := answeredMerge
			for n := 1; n <= 2; n++ {
				to := commitN(n)
				clean(ws, baseN(n), from, to)
				f.SetMergeability(forge.Mergeability{Head: from, Base: baseN(n), State: "behind", Behind: 1})
				want(t, e.advance(t, ev), deliverWait)
				_, body := f.Text(repoRemote, 1)
				if !strings.Contains(body, "## Summary\n") {
					t.Errorf("rebase %d: body lost its Summary:\n%s", n, body)
				}
				if got, want := body, withRebased(t, published, head, rebasedLine("deliver", baseN(n), to)); got != want {
					t.Fatalf("rebase %d: body:\n%s\nwant publish's body plus the latest rebase line:\n%s", n, got, want)
				}
				if c := strings.Count(body, "- Rebased:"); c != 1 {
					t.Errorf("rebase %d: %d rebase lines, want 1", n, c)
				}
				f.SetChecks(repoRemote, to, ciPassed...)
				from, ev = to, interp.Event{Kind: interp.TaskCreated}
			}
		})
	}
}
