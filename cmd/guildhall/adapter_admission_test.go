package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cross the production executable boundary: test-process detection must not affect admission.
func TestBinaryRefusesImplicitFake(t *testing.T) {
	d := newBinDirector(t, filepath.Join(runCopy(t), "config.toml"))
	original, err := os.ReadFile(d.cfg)
	if err != nil {
		t.Fatal(err)
	}
	d.newTask() // Explicit fake remains admitted by the real binary.
	missing := bytes.Replace(original, []byte("default_adapter = \"fake\"\n"), nil, 1)
	if err := os.WriteFile(d.cfg, missing, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"task", "new", "--store", d.store, "--config", d.cfg, "--repo", "app", "--title", "No fake", "t-2"},
		append([]string{"task", "start"}, append(d.flags, "t-1")...),
	} {
		before, err := os.ReadFile(d.store)
		if err != nil {
			t.Fatal(err)
		}
		out, stderr, err := d.try("", args...)
		if err == nil || out != "" || !strings.Contains(stderr, `step "plan"`) || !strings.Contains(stderr, "profile") || !strings.Contains(stderr, "default_adapter") {
			t.Fatalf("%v: err %v out %q stderr %q", args, err, out, stderr)
		}
		after, err := os.ReadFile(d.store)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("refusal changed store: %v", err)
		}
	}
	if err := os.WriteFile(d.cfg, original, 0600); err != nil {
		t.Fatal(err)
	}
	d.want(d.advance([]string{"task", "start"}, "t-1"), `{"wait":"attempt","step":"plan","attempt":1}`)
	d.done("plan", 1) // Explicit fake actually produces a result, not merely admission.
}
