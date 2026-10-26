package testenv_test

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestWaitForObservesTheCondition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		testenv.WaitFor(t, "already ready", func() bool { return true })
		if time.Since(start) != 0 {
			t.Fatal("waited for an already-ready condition")
		}
		calls := 0
		testenv.WaitFor(t, "third check", func() bool { calls++; return calls == 3 })
		if calls != 3 {
			t.Fatalf("checks = %d, want 3", calls)
		}
	})
}

type failingWait struct{ testing.TB }

func (f failingWait) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

func TestWaitForNamesTheMissingEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		defer func() {
			got := recover()
			if got == nil || !strings.Contains(fmt.Sprint(got), "waiting for worker ready") {
				t.Fatalf("failure = %v", got)
			}
		}()
		testenv.WaitFor(failingWait{t}, "worker ready", func() bool { return false })
	})
}
