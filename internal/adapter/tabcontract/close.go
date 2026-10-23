package tabcontract

import (
	"context"
	"testing"
)

type CloseFixture struct {
	Close   func(context.Context) error
	Present func() bool
}

func TestIdleClose(t *testing.T, setup func(*testing.T, string) CloseFixture) {
	t.Helper()
	for _, scenario := range []string{"idle", "busy", "transient-busy", "cancelled", "session", "workspace", "tab", "pane", "closed"} {
		t.Run(scenario, func(t *testing.T) {
			f := setup(t, scenario)
			ctx := context.Background()
			if scenario == "closed" {
				if err := f.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := f.Close(ctx)
			if scenario == "idle" {
				if err != nil {
					t.Fatalf("idle close: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe close succeeded")
			}
			present := f.Present()
			if want := scenario != "idle" && scenario != "closed"; present != want {
				t.Errorf("tab present = %v, want %v", present, want)
			}
		})
	}
}
