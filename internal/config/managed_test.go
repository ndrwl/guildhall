package config

import (
	"strings"
	"testing"
)

func TestCombinedManagedIntegrity(t *testing.T) {
	local := []byte("schema_version = 1\nmode = \"steward\"\n[private_config]\nlocation = '/private'\n[local]\nstyle = 'local'\n")
	managed, err := EncodeManaged(map[string]ManagedSetting{"style": {Value: "central", Overridable: true}})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseCombined(local, managed)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := cfg.Setting("style"); v != "local" {
		t.Fatal(v)
	}
	for i := 0; i < len(managed); i++ {
		if _, err := ParseCombined(local, managed[:i]); err == nil {
			t.Fatalf("accepted prefix %d", i)
		}
	}
	locked, _ := EncodeManaged(map[string]ManagedSetting{"style": {Value: "central"}})
	if _, err := ParseCombined(local, locked); err == nil {
		t.Fatal("accepted forbidden override")
	}
	inline := append(local, []byte("\n[managed.other]\nvalue = 'old'\n")...)
	if _, err := ParseCombined(inline, managed); err == nil || !strings.Contains(err.Error(), "move") {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := Parse(inline); err != nil {
		t.Fatalf("legacy Parse: %v", err)
	}
}

func TestOwnerPathDuplicates(t *testing.T) {
	for _, pair := range [][2]string{{`C:\src\tool`, `C:/src/./other/../tool`}, {`c:/src/tool`, `C:\src\tool`}, {`/src/tool`, `/src/other/../tool`}} {
		data := "schema_version = 1\nmode = 'director'\nname = 'tools'\n"
		for i, p := range pair {
			data += "[[repository]]\nid = 'repo" + string(rune('a'+i)) + "'\npath = '" + p + "'\nremote = 'git@example:repo'\nowner_machine = 'owner'\n"
		}
		if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "already used") {
			t.Fatalf("%v: %v", pair, err)
		}
	}
}
