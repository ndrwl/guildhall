package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSetLocalKeepsEveryOtherByte(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		values         map[string]string
	}{
		{
			name:   "replace and add",
			in:     "schema_version = 1 # top\n\n[local]\n# the cap\n  max_running_attempts = \"9\" # mine\nlock_wait = '5s'\n\n[[repository]]\nid = \"app\"\n",
			values: map[string]string{"max_running_attempts": "2", "max_attempts_per_day": "10"},
			want:   "schema_version = 1 # top\n\n[local]\n# the cap\n  max_running_attempts = \"2\"\nlock_wait = '5s'\nmax_attempts_per_day = \"10\"\n\n[[repository]]\nid = \"app\"\n",
		},
		{
			name:   "no local table",
			in:     "schema_version = 1\nmode = \"director\"",
			values: map[string]string{"b": "x\"y", "a": "1"},
			want:   "schema_version = 1\nmode = \"director\"\n\n[local]\na = \"1\"\nb = \"x\\\"y\"\n",
		},
		{
			name:   "crlf",
			in:     "[local]\r\nx = \"1\"\r\n",
			values: map[string]string{"y": "2"},
			want:   "[local]\r\nx = \"1\"\r\ny = \"2\"\r\n",
		},
		{
			name:   "nothing to set",
			in:     "anything at all",
			values: nil,
			want:   "anything at all",
		},
	} {
		got, err := SetLocal([]byte(c.in), c.values)
		if err != nil || string(got) != c.want {
			t.Errorf("%s: SetLocal = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestSetLocalRefusesUneditable(t *testing.T) {
	values := map[string]string{"lock_wait": "1s"}
	for name, in := range map[string]string{
		"inline":         "schema_version = 1\nlocal = { lock_wait = \"5s\" }\n",
		"dotted":         "schema_version = 1\nlocal.lock_wait = \"5s\"\n",
		"twice":          "[local]\na = \"1\"\n[local]\nb = \"2\"\n",
		"nested":         "[local]\na = \"1\"\n[local.more]\nb = \"2\"\n",
		"quoted key":     "[local]\n\"lock_wait\" = \"5s\"\n",
		"multi-line":     "[local]\nlock_wait = \"\"\"\n5s\"\"\"\n",
		"not a string":   "[local]\nlock_wait = 5\n",
		"not toml":       "[local\n",
		"dotted in body": "[local]\nlock.wait = \"1\"\n",
	} {
		if got, err := SetLocal([]byte(in), values); err == nil {
			t.Errorf("%s: SetLocal = %q, want a refusal", name, got)
		}
	}
	if _, err := SetLocal([]byte("schema_version = 1\nlocal = { a = \"1\" }\n"), values); err == nil || !strings.Contains(err.Error(), "inline") {
		t.Errorf("inline refusal = %v", err)
	}
}

func TestTOMLStringRoundTrips(t *testing.T) {
	for _, v := range []string{"", "plain", `back\slash "quoted"`, "tab\tnew\nline\x01\x7f", "ünïcode"} {
		out, err := SetLocal([]byte("[local]\n"), map[string]string{"k": v})
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Local map[string]string }
		if _, err := toml.Decode(string(out), &got); err != nil || got.Local["k"] != v {
			t.Errorf("%q round-tripped to %q, %v (%s)", v, got.Local["k"], err, out)
		}
	}
}
