package config

import (
	"strings"
	"testing"
	"time"
)

func TestArtisanWorkspaceCloseAfter(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 time.Duration
	}{
		{name: "unset is the default", want: DefaultArtisanWorkspaceCloseAfter},
		{name: "a local duration", local: `artisan_workspace_close_after = "30s"`, want: 30 * time.Second},
		{name: "zero never closes", local: `artisan_workspace_close_after = "0"`, want: 0},
		{name: "zero seconds never closes", local: `artisan_workspace_close_after = "0s"`, want: 0},
		{name: "a managed duration", managed: "[managed.artisan_workspace_close_after]\nvalue = \"10m\"\n", want: 10 * time.Minute},
		{name: "a managed zero", managed: "[managed.artisan_workspace_close_after]\nvalue = \"0\"\n", want: 0},
		{name: "an overridable managed duration replaced locally", local: `artisan_workspace_close_after = "2m"`,
			managed: "[managed.artisan_workspace_close_after]\nvalue = \"10m\"\noverridable = true\n", want: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.ArtisanWorkspaceCloseAfter(); got != tc.want || err != nil {
				t.Errorf("ArtisanWorkspaceCloseAfter() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultArtisanWorkspaceCloseAfter != 5*time.Minute {
		t.Errorf("DefaultArtisanWorkspaceCloseAfter = %v, want 5m", DefaultArtisanWorkspaceCloseAfter)
	}
}

func TestArtisanWorkspaceCloseAfterRefusesAnInvalidDuration(t *testing.T) {
	for _, bad := range []string{
		`[local]` + "\n" + `artisan_workspace_close_after = "soon"`,
		`[local]` + "\n" + `artisan_workspace_close_after = "-1m"`,
		`[local]` + "\n" + `artisan_workspace_close_after = "60"`,
		"[managed.artisan_workspace_close_after]\nvalue = \"later\"\n[local]",
		"[managed.artisan_workspace_close_after]\nvalue = \"-5m\"\n[local]",
	} {
		data := strings.Replace(director(t), "[local]", bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "artisan_workspace_close_after") {
			t.Errorf("%q: Parse error %v, want one naming artisan_workspace_close_after", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{ArtisanWorkspaceCloseAfterKey: "soon"}}
	if _, err := cfg.ArtisanWorkspaceCloseAfter(); err == nil {
		t.Error("ArtisanWorkspaceCloseAfter of an unparsed invalid value succeeded")
	}
}
