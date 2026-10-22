package testenv_test

import (
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestCeiling(t *testing.T) {
	cases := []struct {
		name, ci string
		d, want  time.Duration
	}{
		{name: "unset keeps the ceiling", d: 2 * time.Second, want: 2 * time.Second},
		{name: "unset keeps a long ceiling", d: 5 * time.Minute, want: 5 * time.Minute},
		{name: "a CI runner scales it", ci: "true", d: 2 * time.Second, want: 2 * time.Second * testenv.CISlowdown},
		{name: "any CI value scales it", ci: "1", d: 2 * time.Second, want: 2 * time.Second * testenv.CISlowdown},
		{name: "scaling stops at the cap", ci: "true", d: time.Minute, want: testenv.CIMaxCeiling},
		{name: "a ceiling at the cap stays", ci: "true", d: testenv.CIMaxCeiling, want: testenv.CIMaxCeiling},
		{name: "a ceiling past the cap stays", ci: "true", d: 5 * time.Minute, want: 5 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(testenv.CIEnv, c.ci)
			if got := testenv.Ceiling(c.d); got != c.want {
				t.Errorf("Ceiling(%v) with %s=%q = %v; want %v", c.d, testenv.CIEnv, c.ci, got, c.want)
			}
		})
	}
}
