package interp

import (
	"reflect"
	"testing"
)

// TestTestTimeout: a go test timeout is read from a job log's panic, its "running tests:" block and the package's
// FAIL line, with or without the job log's timestamps; a tail cut before the block still names the timeout and
// package, and a log with no timeout panic has none.
func TestTestTimeout(t *testing.T) {
	for _, c := range []struct {
		name string
		log  string
		want *rerunTimeout
	}{{
		name: "job log",
		log: "2026-11-04T13:50:16.4680249Z ok  \tgithub.com/ndrwl/guildhall/internal/herdr\t18.062s\r\n" +
			"2026-11-04T13:54:15.3668618Z panic: test timed out after 10m0s\r\n" +
			"2026-11-04T13:54:15.3669707Z \trunning tests:\r\n" +
			"2026-11-04T13:54:15.3670245Z \t\tTestValidateTier2 (0s)\r\n" +
			"2026-11-04T13:54:15.3670584Z \r\n" +
			"2026-11-04T13:54:15.3670830Z goroutine 16280 [running]:\r\n" +
			"2026-11-04T13:54:15.3722402Z FAIL\tgithub.com/ndrwl/guildhall/internal/interp\t600.123s\r\n" +
			"2026-11-04T13:54:15.3722895Z ok  \tgithub.com/ndrwl/guildhall/internal/interp/interptest\t3.804s\r\n" +
			"2026-11-04T13:54:15.3723347Z ##[error]Process completed with exit code 1.\r\n",
		want: &rerunTimeout{After: "10m0s", Package: "github.com/ndrwl/guildhall/internal/interp", Running: []string{"TestValidateTier2"}},
	}, {
		name: "local with a subtest",
		log: "panic: test timed out after 30s\n\trunning tests:\n\t\tTestA (30s)\n\t\tTestA/off/store (29s)\n\t\tTestA (30s)\n\n" +
			"goroutine 1 [running]:\nFAIL\texample.com/m/p\t30.012s\nFAIL\texample.com/m/q\t1.000s\n",
		want: &rerunTimeout{After: "30s", Package: "example.com/m/p", Running: []string{"TestA", "TestA/off/store"}},
	}, {
		name: "block cut from the tail",
		log: "2026-11-04T13:54:15.3668618Z panic: test timed out after 10m0s\n" +
			"2026-11-04T13:54:15.3730000Z \tC:/hostedtoolcache/windows/go/1.27.3/x64/src/testing/testing.go:2961 +0x34a\n" +
			"2026-11-04T13:54:15.3722402Z FAIL\tgithub.com/ndrwl/guildhall/internal/interp\t600.123s\n",
		want: &rerunTimeout{After: "10m0s", Package: "github.com/ndrwl/guildhall/internal/interp", Running: []string{}},
	}, {
		name: "no FAIL line",
		log:  "panic: test timed out after 1m0s\n\trunning tests:\n\t\tTestB (1m0s)\n",
		want: &rerunTimeout{After: "1m0s", Running: []string{"TestB"}},
	}, {
		name: "failed tests only",
		log:  "2026-10-29T10:00:03.0000000Z --- FAIL: TestX (0.01s)\n2026-10-29T10:00:03.1000000Z FAIL\texample.com/m/p\t0.020s\n",
	}, {
		name: "empty",
	}} {
		t.Run(c.name, func(t *testing.T) {
			if got := testTimeout([]byte(c.log)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("testTimeout = %+v, want %+v", got, c.want)
			}
		})
	}
}

// TestRerunTimeoutText: a timeout's flake message text names its package when known, and says when the tests it
// was running are not in the log's tail.
func TestRerunTimeoutText(t *testing.T) {
	for _, c := range []struct {
		t    rerunTimeout
		want string
	}{
		{rerunTimeout{After: "10m0s", Package: "p", Running: []string{"TestA", "TestA/b"}}, "package p timed out after 10m0s, running TestA, TestA/b"},
		{rerunTimeout{After: "10m0s", Running: []string{"TestA"}}, "timed out after 10m0s, running TestA"},
		{rerunTimeout{After: "10m0s", Package: "p", Running: []string{}}, "package p timed out after 10m0s, running tests not in the log tail"},
	} {
		if got := c.t.text(); got != c.want {
			t.Errorf("text(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}
