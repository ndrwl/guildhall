package config

import (
	"strings"
	"testing"
)

func TestCILogLimit(t *testing.T) {
	for _, tc := range []struct {
		name, local, managed string
		want                 int64
	}{
		{name: "unset is the default", want: DefaultCILogLimit},
		{name: "MiB", local: `ci_log_limit = "5MiB"`, want: 5 << 20},
		{name: "MB is 1024-based", local: `ci_log_limit = "5MB"`, want: 5 << 20},
		{name: "KiB", local: `ci_log_limit = "512KiB"`, want: 512 << 10},
		{name: "KB is 1024-based", local: `ci_log_limit = "64KB"`, want: 64 << 10},
		{name: "whole bytes", local: `ci_log_limit = "1048576"`, want: 1 << 20},
		{name: "the upper bound", local: `ci_log_limit = "100MiB"`, want: 100 << 20},
		{name: "zero fetches no logs", local: `ci_log_limit = "0"`, want: 0},
		{name: "a managed limit", managed: "[managed.ci_log_limit]\nvalue = \"1MiB\"\n", want: 1 << 20},
		{name: "an overridable managed limit replaced locally", local: `ci_log_limit = "2MiB"`,
			managed: "[managed.ci_log_limit]\nvalue = \"1MiB\"\noverridable = true\n", want: 2 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := strings.Replace(director(t), `notes_dir = "notes"`, `notes_dir = "notes"`+"\n"+tc.local, 1)
			data = strings.Replace(data, "[local]", tc.managed+"[local]", 1)
			cfg, err := Parse([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if got, err := cfg.CILogLimit(); got != tc.want || err != nil {
				t.Errorf("CILogLimit() = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	if DefaultCILogLimit != 5<<20 {
		t.Errorf("DefaultCILogLimit = %d, want 5 MiB", DefaultCILogLimit)
	}
}

func TestCILogLimitRefusesAnInvalidValue(t *testing.T) {
	for _, v := range []string{"-1", "1.5MiB", " 5MiB", "5MiB ", "5 MiB", "5GiB", "5mib", "MiB", "", "63KiB", "101MiB", "65535", "+5MiB", "0x10000", "9999999999999999MiB"} {
		for _, bad := range []string{
			"[local]\nci_log_limit = \"" + v + "\"",
			"[managed.ci_log_limit]\nvalue = \"" + v + "\"\n[local]",
		} {
			data := strings.Replace(director(t), "[local]", bad, 1)
			if _, err := Parse([]byte(data)); err == nil || !strings.Contains(err.Error(), "ci_log_limit") {
				t.Errorf("%q: Parse error %v, want one naming ci_log_limit", bad, err)
			}
		}
	}
	cfg := &Config{Local: map[string]string{CILogLimitKey: "lots"}}
	if _, err := cfg.CILogLimit(); err == nil {
		t.Error("CILogLimit of an unparsed invalid value succeeded")
	}
}
