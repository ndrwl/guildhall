package testenv_test

import (
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/testenv"
)

func TestCeiling(t *testing.T) {
	cases := []struct {
		name, ci string
		want     time.Duration
	}{
		{name: "unset keeps the ceiling", want: 2 * time.Second},
		{name: "a CI runner scales it", ci: "true", want: 2 * time.Second * testenv.CISlowdown},
		{name: "any CI value scales it", ci: "1", want: 2 * time.Second * testenv.CISlowdown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(testenv.CIEnv, c.ci)
			if got := testenv.Ceiling(2 * time.Second); got != c.want {
				t.Errorf("Ceiling(2s) with %s=%q = %v; want %v", testenv.CIEnv, c.ci, got, c.want)
			}
		})
	}
}
