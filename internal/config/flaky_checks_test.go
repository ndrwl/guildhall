package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFlakyChecks: flaky_checks is a repository key; unset it resolves to rerun-and-file, and a value other than
// off, rerun or rerun-and-file is refused.
func TestFlakyChecks(t *testing.T) {
	for _, tc := range []struct {
		line string
		want FlakyChecks
	}{
		{"", FlakyChecksRerunAndFile},
		{`flaky_checks = "off"`, FlakyChecksOff},
		{`flaky_checks = "rerun"`, FlakyChecksRerun},
		{`flaky_checks = "rerun-and-file"`, FlakyChecksRerunAndFile},
	} {
		cfg, err := Parse([]byte(strings.Replace(director(t), "no_ci = true\n", "no_ci = true\n"+tc.line+"\n", 1)))
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if got := cfg.Repositories[1].FlakyChecksPolicy(); got != tc.want {
			t.Errorf("%q: FlakyChecksPolicy() = %q, want %q", tc.line, got, tc.want)
		}
		if got := cfg.Repositories[0].FlakyChecksPolicy(); got != DefaultFlakyChecks {
			t.Errorf("%q: the other repository's policy = %q, want the default", tc.line, got)
		}
	}
	for _, bad := range []string{"retry", "on", "Rerun", "rerun_and_file"} {
		_, err := Parse([]byte(strings.Replace(director(t), "no_ci = true\n", "no_ci = true\nflaky_checks = \""+bad+"\"\n", 1)))
		if err == nil || !strings.Contains(err.Error(), `repository "site": flaky_checks "`+bad+`" must be off, rerun or rerun-and-file`) {
			t.Errorf("flaky_checks = %q: err = %v, want it refused", bad, err)
		}
	}
}

// TestFlakyChecksKeepsTheDigest: a config without flaky_checks digests as before the key existed, since the key is
// omitted from JSON when unset.
func TestFlakyChecksKeepsTheDigest(t *testing.T) {
	cfg, err := Parse([]byte(director(t)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"FlakyChecks"`) {
		t.Fatalf("config JSON without flaky_checks = %s", b)
	}
	cfg.Repositories[0].FlakyChecks = FlakyChecksOff
	if b, err := json.Marshal(cfg); err != nil || !strings.Contains(string(b), `"FlakyChecks":"off"`) {
		t.Fatalf("config JSON with flaky_checks = %s, %v", b, err)
	}
}
