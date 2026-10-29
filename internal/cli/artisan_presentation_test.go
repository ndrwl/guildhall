package cli

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestAdvanceAndJudgeLaunchTaskLocked: the advance and the judge hold the task lock for every launch, so the
// Process each launches with is TaskLocked and recovers a stale launching pane claim (d48).
func TestAdvanceAndJudgeLaunchTaskLocked(t *testing.T) {
	if p := taskLockedProcess(nil, "guildhall", "store", "state", "copy", time.Minute, time.Second, nil); !p.TaskLocked || p.Copy != "copy" || p.OpenPresentation == nil {
		t.Fatalf("taskLockedProcess = %+v", p)
	}
	for _, file := range []string{"advance.go", "quality.go"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(b), "Launcher: taskLockedProcess(") != 1 || strings.Contains(string(b), "launch.Process{") {
			t.Errorf("%s does not launch through taskLockedProcess", file)
		}
	}
}
