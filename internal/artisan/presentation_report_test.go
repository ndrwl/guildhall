package artisan_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/artisan"
)

func readEvents(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, artisan.EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func TestLogPresentationReportRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pane := json.RawMessage("{\n  \"agent_status\": \"working\",\n  \"pane_id\": \"w1:p2\"\n}")
	if err := artisan.LogPresentationReport(dir, artisan.PresentationReport{
		Pane: "w1:p2", Exit: 0, Stdout: []byte(`{"result":{"type":"ok"}}`),
		Reports: 2, ReadBacks: 2, RetryErr: errors.New("herdr busy"),
		ReadBackAgentStatus: "working", ReadBackAgent: "artisan", ReadBackPane: pane,
	}); err != nil {
		t.Fatal(err)
	}
	big := []byte(strings.Repeat("x", artisan.PresentationReportLimit+10))
	if err := artisan.LogPresentationReport(dir, artisan.PresentationReport{
		Pane: "w1:p3", Exit: -1, Stdout: big, Err: errors.New("herdr stopped"),
		ReadBackErr: errors.New("skipped: the launch did not settle"),
	}); err != nil {
		t.Fatal(err)
	}
	bigPane := json.RawMessage(`{"pad":"` + string(big) + `"}`)
	if err := artisan.LogPresentationReport(dir, artisan.PresentationReport{Pane: "w1:p4", ReadBackPane: bigPane}); err != nil {
		t.Fatal(err)
	}

	events := readEvents(t, dir)
	if len(events) != 3 {
		t.Fatalf("events = %v", events)
	}
	ok := events[0]
	if ok["event"] != "presentation-report" || ok["at"] == nil || ok["pane"] != "w1:p2" || ok["exit"] != 0.0 || ok["stdout"] != `{"result":{"type":"ok"}}` {
		t.Errorf("success event = %v", ok)
	}
	if ok["readback_agent_status"] != "working" || ok["readback_agent"] != "artisan" || ok["readback_pane"].(map[string]any)["pane_id"] != "w1:p2" {
		t.Errorf("read-back = %v", ok)
	}
	if ok["reports"] != 2.0 || ok["readbacks"] != 2.0 || ok["retry_error"] != "herdr busy" {
		t.Errorf("retries = %v", ok)
	}
	for _, absent := range []string{"stdout_truncated", "error", "readback_error"} {
		if _, has := ok[absent]; has {
			t.Errorf("success event has %s: %v", absent, ok)
		}
	}
	failed := events[1]
	if failed["exit"] != -1.0 || failed["error"] != "herdr stopped" || failed["stdout_truncated"] != true || len(failed["stdout"].(string)) != artisan.PresentationReportLimit || failed["readback_error"] != "skipped: the launch did not settle" {
		t.Errorf("failure event = %v", failed)
	}
	for _, absent := range []string{"readback_pane", "readback_agent", "readback_agent_status", "retry_error"} {
		if _, has := failed[absent]; has {
			t.Errorf("failure event has %s: %v", absent, failed)
		}
	}
	if failed["reports"] != 0.0 || failed["readbacks"] != 0.0 {
		t.Errorf("failure event counts = %v", failed)
	}
	if s, isString := events[2]["readback_pane"].(string); !isString || len(s) != artisan.PresentationReportLimit {
		t.Errorf("oversized pane = %v", events[2]["readback_pane"])
	}
}
