package artisan_test

import (
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

// TestParseAttemptID: the inverse of AttemptKey.AttemptID, splitting at the last "-" with a canonical positive n.
func TestParseAttemptID(t *testing.T) {
	for _, c := range []struct {
		id   string
		step string
		n    int
		ok   bool
	}{
		{"a-1", "a", 1, true},
		{"a-b-12", "a-b", 12, true},
		{"a-0", "", 0, false},
		{"a-01", "", 0, false},
		{"a", "", 0, false},
		{"a-", "", 0, false},
		{"-1", "", 0, false},
	} {
		step, n, ok := artisan.ParseAttemptID(c.id)
		if step != c.step || n != c.n || ok != c.ok {
			t.Errorf("ParseAttemptID(%q) = %q, %d, %v; want %q, %d, %v", c.id, step, n, ok, c.step, c.n, c.ok)
		}
	}
	k := artisan.AttemptKey{Repository: "app", TaskID: "t1", Step: "ci-check", Attempt: 7}
	if step, n, ok := artisan.ParseAttemptID(k.AttemptID()); !ok || step != k.Step || n != k.Attempt {
		t.Errorf("ParseAttemptID(%q) = %q, %d, %v; want the key's step and attempt", k.AttemptID(), step, n, ok)
	}
}
