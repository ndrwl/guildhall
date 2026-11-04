package admit

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ndrwl/guildhall/internal/config"
)

// TestLocalLockWait: admission's store reads wait for the lock_wait config.toml sets, and the default when it sets
// none or its bytes are empty or do not parse.
func TestLocalLockWait(t *testing.T) {
	repo := "[[repository]]\nid = \"app\"\npath = '" + filepath.Join(t.TempDir(), "app") + "'\nremote = \"git@github.com:example/app.git\"\nowner_machine = \"workstation\"\n\n"
	head := "schema_version = 1\nmode = \"director\"\nname = \"alpha\"\n\n" + repo + "[local]\n"
	for _, c := range []struct {
		name  string
		local string
		want  time.Duration
	}{
		{"set", head + "lock_wait = \"5s\"\n", 5 * time.Second},
		{"unset", head, config.DefaultLockWait},
		{"unparsable", head + "lock_wait = \n", config.DefaultLockWait},
		{"invalid wait", head + "lock_wait = \"soon\"\n", config.DefaultLockWait},
		{"empty", "", config.DefaultLockWait},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := localLockWait([]byte(c.local)); got != c.want {
				t.Errorf("localLockWait = %v, want %v", got, c.want)
			}
		})
	}
}
