package interp

import (
	"errors"
	"testing"
)

func TestCheckAdapterUnderTest(t *testing.T) {
	for _, name := range []string{MockAdapter, FakeAdapter} {
		if err := CheckAdapter(name); err != nil {
			t.Errorf("CheckAdapter(%q) = %v, want allowed", name, err)
		}
	}
	for _, name := range []string{"claude", "codex", "", "Mock"} {
		if err := CheckAdapter(name); !errors.Is(err, ErrAdapterRefused) {
			t.Errorf("CheckAdapter(%q) = %v, want ErrAdapterRefused", name, err)
		}
	}
}

func TestCheckAdapterOutsideTests(t *testing.T) {
	for _, name := range []string{"claude", MockAdapter, FakeAdapter} {
		if err := checkAdapter(name, false); err != nil {
			t.Errorf("checkAdapter(%q, outside tests) = %v, want allowed", name, err)
		}
	}
}
