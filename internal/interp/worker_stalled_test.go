package interp_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/artisan"
	"github.com/ndrwl/guildhall/internal/interp"
	"github.com/ndrwl/guildhall/internal/interp/interptest"
	"github.com/ndrwl/guildhall/internal/store"
	"github.com/ndrwl/guildhall/internal/strictjson"
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
			dir := filepath.Join(e.eng.StateDir, "app", "t7", fmt.Sprintf("%s-%d", next.Step, next.Attempt))
			hb := artisan.Heartbeat{Repository: "app", TaskID: "t7", Step: next.Step, Attempt: next.Attempt, Sequence: 1, Time: now.Add(-2 * time.Minute), LastOutputAt: now.Add(-3 * time.Minute), WorkerState: "running"}
			put := func() {
				t.Helper()
				if err := artisan.WriteHeartbeat(dir, hb); err != nil {
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
			// The message is interp.StalledMessage, the one the attempt's launcher sends for the same crossing.
			page, err := e.mailbox.List(store.Recipient{Role: store.RecipientDirector}, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			sent := interp.StalledMessage(hb.Key(), hb.LastOutputAt)
			if m := page.Messages[len(page.Messages)-1]; sent.ID == "" || m.ID != sent.ID || m.Type != sent.Type || m.Text != sent.Text || m.Repository != sent.Repository || m.Task != sent.Task {
				t.Fatalf("engine sent %+v, StalledMessage is %+v", m, sent)
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
			// A heartbeat naming another attempt is not this attempt's evidence.
			now = now.Add(2 * time.Minute)
			hb.Sequence++
			hb.Time, hb.Attempt = now, next.Attempt+1
			put()
			want(t, e.advance(t, ev), next)
			count(2)
			// A newer heartbeat schema is reported, never guessed at.
			if err := os.WriteFile(filepath.Join(dir, artisan.HeartbeatFile), []byte(`{"schema":"guildhall.attempt-heartbeat/2"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			ev.Repository, ev.Task = "app", "t7"
			if _, err := e.eng.Advance(context.Background(), ev); !errors.Is(err, strictjson.ErrNewerSchema) {
				t.Fatalf("Advance with a newer heartbeat = %v, want ErrNewerSchema", err)
			}
			count(2)
		})
	}
}
