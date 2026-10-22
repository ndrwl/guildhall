package config

import (
	"strings"
	"testing"
	"time"
)

func TestLockWait(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 time.Duration
	}{
		{name: "unset is the default", want: DefaultLockWait},
		{name: "a local duration", local: `lock_wait = "2s"`, want: 2 * time.Second},
		{name: "a managed duration", managed: "[managed.lock_wait]\nvalue = \"1m\"\n", want: time.Minute},
		{name: "an overridable managed duration replaced locally", local: `lock_wait = "500ms"`,
			managed: "[managed.lock_wait]\nvalue = \"1m\"\noverridable = true\n", want: 500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.LockWait(); got != tc.want || err != nil {
				t.Errorf("LockWait() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultLockWait != 30*time.Second {
		t.Errorf("DefaultLockWait = %v, want 30s", DefaultLockWait)
	}
}

// TestLockWaitRefusesAnInvalidDuration: a lock wait is positive, since a zero wait would fail every advance that
// meets a held lock.
func TestLockWaitRefusesAnInvalidDuration(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `lock_wait = "soon"`,
		`[local]` + "\n" + `lock_wait = "-1s"`,
		`[local]` + "\n" + `lock_wait = "0s"`,
		`[local]` + "\n" + `lock_wait = "30"`,
		"[managed.lock_wait]\nvalue = \"later\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "lock_wait") {
			t.Errorf("%q: Parse error %v, want one naming lock_wait", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{LockWaitKey: "soon"}}
	if _, err := cfg.LockWait(); err == nil {
		t.Error("LockWait of an unparsed invalid value succeeded")
	}
}
