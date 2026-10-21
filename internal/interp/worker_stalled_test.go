package interp_test

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
)

func TestWorkerStalled(t *testing.T) {
	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			e := newEnv(t, b, planExpectation(t, interptest.Hang()))
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			e.eng.Now = func() time.Time { return now }
			e.eng.StallInterval = time.Minute
			ev := interp.Event{Kind: interp.TaskCreated}
			next := e.advance(t, ev)
			before, _ := e.h.Records.Attempts("app", "t7")
			count := func(want int) {
				t.Helper()
				got := 0
				page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range page.Messages {
					if m.Type == "worker-stalled" {
						got++
					}
				}
				if got != want {
					t.Fatalf("stalls = %d, want %d", got, want)
				}
				after, _ := e.h.Records.Attempts("app", "t7")
				if !reflect.DeepEqual(before, after) {
					t.Fatal("heartbeat observation changed attempts")
				}
			}
			want(t, e.advance(t, ev), next)
			count(0)
			hb := store.Heartbeat{Repository: "app", TaskID: "t7", Step: next.Step, Attempt: next.Attempt, Sequence: 1, Time: now.Add(-2 * time.Minute), LastOutputAt: now.Add(-3 * time.Minute), WorkerState: "running"}
			put := func() {
				t.Helper()
				if _, err := e.h.PutHeartbeat(fmt.Sprint("heartbeat-", hb.Sequence), hb); err != nil {
					t.Fatal(err)
				}
			}
			put()
			want(t, e.advance(t, ev), next)
			count(0)
			hb.Sequence++
			hb.Time = now
			put()
			for range 2 {
				want(t, e.advance(t, ev), next)
				count(1)
			}
			hb.Sequence++
			hb.LastOutputAt = now
			put()
			want(t, e.advance(t, ev), next)
			count(1)
			now = now.Add(2 * time.Minute)
			hb.Sequence++
			hb.Time = now
			put()
			for range 2 {
				want(t, e.advance(t, ev), next)
				count(2)
			}
		})
	}
}
