package spec_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/ndrwl/guildhall/internal/spec"
)

func summaryOf(items string) []byte {
	return []byte("# Title\n\n## Summary\n\n- **Goal:** Do it.\n" + items + "\n## Goal\n\nDo it.\n")
}

// TestSummarySize: the Size item's strict forms parse, with thousands separators and the slice prefix, and anything
// else is ErrSummaryItem.
func TestSummarySize(t *testing.T) {
	for line, want := range map[string]spec.Size{
		"- **Size:** about 120 non-test lines in 4 files.":                {Lines: 120, Files: 4},
		"- **Size:** about 1 non-test line in 1 file":                     {Lines: 1, Files: 1},
		"- **Size:** 2 slices, about 1,150 non-test lines in 32 files.":   {Slices: 2, Lines: 1150, Files: 32},
		"- **Size:** 1 slice, about 12,345,678 non-test lines in 7 files": {Slices: 1, Lines: 12345678, Files: 7},
		"- **Size:** about 1100 non-test lines in 25 files.":              {Lines: 1100, Files: 25},
	} {
		got, err := spec.SummarySize(summaryOf(line))
		if err != nil || got != want {
			t.Errorf("%s: got %+v, %v; want %+v", line, got, err, want)
		}
	}
	for name, data := range map[string][]byte{
		"no summary":        []byte("# Title\n\n## Goal\n\nDo it.\n"),
		"no size":           summaryOf(""),
		"no non-test":       summaryOf("- **Size:** about 120 lines in 4 files."),
		"bad separator":     summaryOf("- **Size:** about 1,15 non-test lines in 4 files."),
		"no about":          summaryOf("- **Size:** 120 non-test lines in 4 files."),
		"range":             summaryOf("- **Size:** about 100-200 non-test lines in 4 files."),
		"two items":         summaryOf("- **Size:** about 1 non-test line in 1 file.\n- **Size:** about 2 non-test lines in 1 file."),
		"trailing prose":    summaryOf("- **Size:** about 120 non-test lines in 4 files, plus tests."),
		"size outside list": []byte("# Title\n\n## Summary\n\n- **Goal:** Do it.\n\n## Goal\n\n- **Size:** about 1 non-test line in 1 file.\n"),
	} {
		if _, err := spec.SummarySize(data); !errors.Is(err, spec.ErrSummaryItem) {
			t.Errorf("%s: err = %v, want ErrSummaryItem", name, err)
		}
	}
}

// TestSummaryTouches: none reads as an empty list, the sensitive areas read in any order, and anything else is
// ErrSummaryItem.
func TestSummaryTouches(t *testing.T) {
	for line, want := range map[string][]string{
		"- **Touches:** none.":                                {},
		"- **Touches:** None":                                 {},
		"- **Touches:** secrets.":                             {"secrets"},
		"- **Touches:** trust and managed settings":           {"trust", "managed settings"},
		"- **Touches:** secrets, managed settings and trust.": {"secrets", "managed settings", "trust"},
	} {
		got, err := spec.SummaryTouches(summaryOf(line))
		if err != nil || got == nil || !slices.Equal(got, want) {
			t.Errorf("%s: got %q, %v; want %q", line, got, err, want)
		}
	}
	for name, data := range map[string][]byte{
		"no touches":   summaryOf(""),
		"empty":        summaryOf("- **Touches:**"),
		"unknown area": summaryOf("- **Touches:** the database."),
		"twice":        summaryOf("- **Touches:** trust and trust."),
		"none and one": summaryOf("- **Touches:** none, secrets."),
		"prose":        summaryOf("- **Touches:** nothing sensitive."),
	} {
		if _, err := spec.SummaryTouches(data); !errors.Is(err, spec.ErrSummaryItem) {
			t.Errorf("%s: err = %v, want ErrSummaryItem", name, err)
		}
	}
}

// TestSummaryValidItems: the Summary example the plan prompt shows has a Size and a Touches item Guildhall reads.
func TestSummaryValidItems(t *testing.T) {
	if s, err := spec.SummarySize([]byte(spec.SummaryValid)); err != nil || s != (spec.Size{Slices: 2, Lines: 1100, Files: 25}) {
		t.Errorf("SummaryValid size = %+v, %v", s, err)
	}
	if got, err := spec.SummaryTouches([]byte(spec.SummaryValid)); err != nil || len(got) != 0 {
		t.Errorf("SummaryValid touches = %q, %v", got, err)
	}
}
