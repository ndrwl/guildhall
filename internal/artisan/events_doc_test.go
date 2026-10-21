package artisan

import (
	"os"
	"strings"
	"testing"
)

func TestEventsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/artisan.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "\n### Events\n")
	if !ok {
		t.Fatal("docs/artisan.md has no Events section")
	}
	section, _, _ = strings.Cut(section, "\n### ")
	section, _, _ = strings.Cut(section, "\n## ")
	_, table, ok := strings.Cut(section, "| Event | When | Fields |\n|---|---|---|\n")
	if !ok {
		t.Fatal("Events section has no event table")
	}
	documented := map[string]bool{}
	for _, row := range strings.Split(table, "\n") {
		if !strings.HasPrefix(row, "|") {
			break
		}
		cells := strings.Split(row, "|")
		if len(cells) != 5 {
			t.Fatalf("invalid event table row: %s", row)
		}
		cell := strings.TrimSpace(cells[1])
		if len(cell) < 3 || cell[0] != '`' || cell[len(cell)-1] != '`' {
			t.Fatalf("invalid event name: %s", cell)
		}
		name := cell[1 : len(cell)-1]
		if documented[name] {
			t.Errorf("duplicate documented event %q", name)
		}
		documented[name] = true
	}
	if len(eventNames) == 0 {
		t.Fatal("empty launcher event vocabulary")
	}
	known := map[string]bool{}
	for _, event := range eventNames {
		name := event.String()
		if known[name] {
			t.Errorf("duplicate launcher event %q", name)
		}
		known[name] = true
		if !documented[name] {
			t.Errorf("launcher event %q missing from documentation", name)
		}
	}
	for name := range documented {
		if !known[name] {
			t.Errorf("documented event %q missing from launcher vocabulary", name)
		}
	}
}
