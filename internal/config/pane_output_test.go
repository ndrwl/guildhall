package config

import (
	"strings"
	"testing"
)

// TestArtisanPaneOutput: an Artisan pane shows the Workbench unless artisan_pane_output says plain.
func TestArtisanPaneOutput(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed, want string
	}{
		{name: "unset is the Workbench", want: "workbench"},
		{name: "a local plain", local: `artisan_pane_output = "plain"`, want: "plain"},
		{name: "a local workbench", local: `artisan_pane_output = "workbench"`, want: "workbench"},
		{name: "a managed plain", managed: "[managed.artisan_pane_output]\nvalue = \"plain\"\n", want: "plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.ArtisanPaneOutput(); got != tc.want || err != nil {
				t.Errorf("ArtisanPaneOutput() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestArtisanPaneOutputRefusesAnUnknownMode(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `artisan_pane_output = "fancy"`,
		`[local]` + "\n" + `artisan_pane_output = "Plain"`,
		"[managed.artisan_pane_output]\nvalue = \"\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "artisan_pane_output") {
			t.Errorf("%q: Parse error %v, want one naming artisan_pane_output", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{ArtisanPaneOutputKey: "fancy"}}
	if _, err := cfg.ArtisanPaneOutput(); err == nil {
		t.Error("ArtisanPaneOutput of an unparsed invalid value succeeded")
	}
}
