package artisan_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestEventsDocumented pins every event name the launcher writes to the
// events table in docs/artisan.md, so a reader of events.jsonl has a contract.
func TestEventsDocumented(t *testing.T) {
	src, err := os.ReadFile("launcher.go")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../../docs/artisan.md")
	if err != nil {
		t.Fatal(err)
	}
	_, events, ok := strings.Cut(string(doc), "\n### Events\n")
	if !ok {
		t.Fatal("docs/artisan.md has no Events section")
	}
	events, _, _ = strings.Cut(events, "\n## ")
	names := regexp.MustCompile(`l\.event\("([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(names) == 0 {
		t.Fatal("no l.event calls found in launcher.go")
	}
	for _, m := range names {
		if !strings.Contains(events, "| `"+m[1]+"` |") {
			t.Errorf("event %q is written by launcher.go but missing from the docs/artisan.md events table", m[1])
		}
	}
}
