package atomicfile

import (
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

var (
	errRace      = errors.New("held by another process")
	errPermanent = errors.New("permission denied")
)

func isRace(err error) bool { return errors.Is(err, errRace) }

func TestRetry(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails []error // errors returned before op succeeds
		want  error
		calls int
	}{
		{"success", nil, nil, 1},
		{"one race", []error{errRace}, nil, 2},
		{"two races", []error{errRace, errRace}, nil, 3},
		{"permanent", []error{errPermanent}, errPermanent, 1},
		{"race then permanent", []error{errRace, errPermanent}, errPermanent, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := retry(func() error {
				calls++
				if calls <= len(tc.fails) {
					return tc.fails[calls-1]
				}
				return nil
			}, isRace)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) || calls != tc.calls {
				t.Fatalf("retry = %v after %d calls, want %v after %d", err, calls, tc.want, tc.calls)
			}
		})
	}
}

func TestRetryIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		err := retry(func() error { calls++; return errRace }, isRace)
		elapsed := time.Since(start)
		if !errors.Is(err, errRace) || elapsed < patience || elapsed > patience+time.Second {
			t.Fatalf("retry of a lasting race = %v after %v, want the race after about %v", err, elapsed, patience)
		}
		// The first call and the call exactly at the deadline precede expiry.
		if max := int(patience/interval) + 2; calls < 2 || calls > max {
			t.Fatalf("retry called op %d times, want between 2 and %d", calls, max)
		}
	})
}
