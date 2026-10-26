package interp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/forge"
	"github.com/ndrwl/guildhall/internal/interp"
)

func TestMergeabilityRetainsDirectorWaitDuringRefresh(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e, f := newValidateEnv(t, b)
			e.eng.Config.Repositories[0].NoCI = true // a conflicting head must not pass just because checks are absent
			now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
			e.eng.Now = func() time.Time { return now }
			e.eng.ChecksRefresh = time.Minute
			f.SetMergeability(forge.Mergeability{Head: head, Base: baseHead, State: "conflicting", Behind: 1})
			director := interp.Next{Wait: interp.WaitDirector, Step: "validate"}
			want(t, e.toValidate(t), director)
			n := len(e.inbox(t))
			f.SetMergeability(forge.Mergeability{Head: head, Base: strings.Repeat("c", 40), State: "conflicting", Behind: 3})
			now = now.Add(time.Minute)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			if len(e.inbox(t)) != n {
				t.Fatal("moving base duplicated report")
			}
			f.SetMergeability(forge.Mergeability{})
			now = now.Add(59 * time.Second)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), director)
			now = now.Add(time.Second)
			want(t, e.advance(t, interp.Event{Kind: interp.TaskCreated}), interp.Next{Wait: interp.WaitDecision, Step: "deliver", Decision: "deliver-1"})
		})
	}
}
