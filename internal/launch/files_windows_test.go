package launch_test

import (
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"golang.org/x/sys/windows"

	"github.com/ndrwl/guildhall/internal/adapter"
	"github.com/ndrwl/guildhall/internal/launch"
)

func TestFilesReadWaitsForExclusiveHolder(t *testing.T) {
	for _, method := range []string{"Result", "ValidResult"} {
		t.Run(method, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := launch.Files{ResultFile: filepath.Join(t.TempDir(), "result.json"), Identity: id}
				if err := os.WriteFile(f.ResultFile, []byte(resultBody(t, id, adapter.StatusSucceeded)), 0o600); err != nil {
					t.Fatal(err)
				}
				name, err := windows.UTF16PtrFromString(f.ResultFile)
				if err != nil {
					t.Fatal(err)
				}
				h, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				if err != nil {
					t.Fatal(err)
				}
				released := make(chan error, 1)
				time.AfterFunc(100*time.Millisecond, func() { released <- windows.CloseHandle(h) })
				defer func() {
					if err := <-released; err != nil {
						t.Errorf("release exclusive result handle: %v", err)
					}
				}()
				switch method {
				case "Result":
					r, err := f.Result()
					if err != nil || r.Identity != id || r.Status != adapter.StatusSucceeded {
						t.Fatalf("Result while held = %+v, %v; want published result", r, err)
					}
				case "ValidResult":
					if valid, err := f.ValidResult(); err != nil || !valid {
						t.Fatalf("ValidResult while held = %v, %v; want true", valid, err)
					}
				}
			})
		})
	}
}
