package config

import (
	"strings"
	"testing"
)

func TestEffectiveEffort(t *testing.T) {
	for _, tc := range []struct{ suffix, want string }{
		{"", "medium"},
		{"\n[local]\ndefault_effort = 'low'\n", "low"},
		{"\n[managed.default_effort]\nvalue = 'high'\n", "high"},
		{"\n[managed.default_effort]\nvalue = 'high'\noverridable = true\n[local]\ndefault_effort = 'max'\n", "max"},
	} {
		// The fixture already has a local table; use a minimal standalone config.
		base := strings.Split(director(t), "[managed.")[0] + "\n[[repository]]\nid='app'\npath='/app'\nremote='git@github.com:example/app.git'\nowner_machine='host'\n"
		cfg, err := Parse([]byte(base + tc.suffix))
		if err != nil {
			t.Fatal(err)
		}
		if got := cfg.EffectiveEffort(""); got != tc.want {
			t.Errorf("default = %q, want %q", got, tc.want)
		}
		if got := cfg.EffectiveEffort("xhigh"); got != "xhigh" {
			t.Errorf("explicit = %q", got)
		}
	}
}

func TestDefaultEffortRefusals(t *testing.T) {
	for _, value := range []string{"", "High", " high", "extreme"} {
		for _, section := range []string{"local", "managed"} {
			suffix := "\n[local]\ndefault_effort = '" + value + "'\n"
			if section == "managed" {
				suffix = "\n[managed.default_effort]\nvalue = '" + value + "'\noverridable = true\n[local]\ndefault_effort='medium'\n"
			}
			base := strings.Split(director(t), "[managed.")[0] + "\n[[repository]]\nid='app'\npath='/app'\nremote='git@github.com:example/app.git'\nowner_machine='host'\n"
			if _, err := Parse([]byte(base + suffix)); err == nil || !strings.Contains(err.Error(), "default_effort") {
				t.Errorf("%s %q: %v", section, value, err)
			}
		}
	}
}

func TestCombinedEffortPrecedence(t *testing.T) {
	base := strings.Split(director(t), "[managed.")[0] + "\n[[repository]]\nid='app'\npath='/app'\nremote='git@github.com:example/app.git'\nowner_machine='host'\n"
	for _, override := range []bool{false, true} {
		managed, err := EncodeManaged(map[string]ManagedSetting{"default_effort": {Value: "high", Overridable: override}})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseCombined([]byte(base), managed)
		if err != nil || cfg.EffectiveEffort("") != "high" {
			t.Fatalf("combined = %+v, %v", cfg, err)
		}
		cfg, err = ParseCombined([]byte(base+"\n[local]\ndefault_effort='low'\n"), managed)
		if !override {
			if err == nil {
				t.Fatal("locked setting accepted")
			}
			continue
		}
		if err != nil || cfg.EffectiveEffort("") != "low" {
			t.Fatalf("override = %+v, %v", cfg, err)
		}
	}
}
