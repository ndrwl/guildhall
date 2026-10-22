package config

import (
	"strings"
	"testing"
)

func TestHerdrSession(t *testing.T) {
	for _, tc := range []struct{ name, local, managed, want string }{
		{name: "unset is none"},
		{name: "a local session", local: `herdr_session = "guild-lab"`, want: "guild-lab"},
		{name: "a managed session", managed: "[managed.herdr_session]\nvalue = \"fleet_1\"\n", want: "fleet_1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.HerdrSession(); got != tc.want || err != nil {
				t.Errorf("HerdrSession() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// TestHerdrSessionRefusesAnUnusableName refuses Herdr's default session, which belongs to the Guildmaster's own
// terminal, and any name Herdr would read as something other than a session name.
func TestHerdrSessionRefusesAnUnusableName(t *testing.T) {
	for _, bad := range []string{`"default"`, `""`, `"-lab"`, `"my lab"`, `"lab/1"`} {
		data := strings.Replace(director(t), "[local]", "[local]\nherdr_session = "+bad, 1)
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "herdr_session") {
			t.Errorf("%s: Parse error %v, want one naming herdr_session", bad, err)
		}
	}
	cfg := &Config{Local: map[string]string{HerdrSessionKey: "default"}}
	if _, err := cfg.HerdrSession(); err == nil {
		t.Error("HerdrSession of an unparsed default session succeeded")
	}
}
