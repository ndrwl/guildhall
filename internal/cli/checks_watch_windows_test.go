package cli

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Every concurrent next.json reader must survive a Windows rename's temporary
// exclusive hold and return the persisted task state once it is released.
func TestNextReadersWaitForExclusiveHolder(t *testing.T) {
	d := newRunDirector(t)
	const body = `{"wait":"checks","step":"validate"}`
	writeNext(t, d.state, "t-1", body+"\n")
	path := filepath.Join(d.state, "app", "t-1", nextFile)
	for _, reader := range []string{"status", "watcher", "supervisor-test"} {
		t.Run(reader, func(t *testing.T) {
			name, err := windows.UTF16PtrFromString(path)
			if err != nil {
				t.Fatal(err)
			}
			h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatal(err)
			}
			released := make(chan struct{})
			time.AfterFunc(50*time.Millisecond, func() {
				_ = windows.CloseHandle(h)
				close(released)
			})
			defer func() { <-released }()
			switch reader {
			case "status":
				got, why := readNext(path)
				if strings.TrimSpace(string(got)) != body || why != "" {
					t.Fatalf("readNext = %s, %q", got, why)
				}
			case "watcher":
				got, err := checksWaiting(d.store, d.state)()
				if err != nil || !slices.Equal(got, []taskRef{t1}) {
					t.Fatalf("checksWaiting = %v, %v", got, err)
				}
			case "supervisor-test":
				if got := strings.TrimSpace(readFileString(t, path)); got != body {
					t.Fatalf("readFileString = %q", got)
				}
			}
		})
	}
}
