package adapter

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testID = Identity{TaskID: "task-1", AttemptID: "attempt-1"}

const validResult = `{
  "schema": "guildhall.artisan-result",
  "version": 1,
  "task_id": "task-1",
  "attempt_id": "attempt-1",
  "status": "succeeded",
  "summary": "did the work",
  "outputs": [{"path": "docs/notes.md", "description": "notes"}],
  "landed_ref": "refs/heads/task-1"
}`

func TestParseResultAcceptsValidResult(t *testing.T) {
	r, err := ParseResult([]byte(validResult), testID)
	if err != nil {
		t.Fatalf("ParseResult = %v", err)
	}
	if r.Status != StatusSucceeded || r.Summary != "did the work" || len(r.Outputs) != 1 || r.Outputs[0].Path != "docs/notes.md" || r.LandedRef != "refs/heads/task-1" {
		t.Errorf("result = %+v", r)
	}
	failed := strings.Replace(validResult, `"succeeded"`, `"failed"`, 1)
	if r, err := ParseResult([]byte(failed), testID); err != nil || r.Status != StatusFailed {
		t.Errorf("failed result = %+v, %v", r, err)
	}
}

func TestParseResultRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":            ``,
		"truncated":        validResult[:40],
		"not object":       `[]`,
		"trailing data":    validResult + `{}`,
		"unknown field":    strings.Replace(validResult, `"summary"`, `"extra": 1, "summary"`, 1),
		"wrong schema":     strings.Replace(validResult, `guildhall.artisan-result`, `other`, 1),
		"version zero":     strings.Replace(validResult, `"version": 1`, `"version": 0`, 1),
		"version string":   strings.Replace(validResult, `"version": 1`, `"version": "1"`, 1),
		"bad status":       strings.Replace(validResult, `"succeeded"`, `"done"`, 1),
		"empty summary":    strings.Replace(validResult, `"did the work"`, `""`, 1),
		"absolute output":  strings.Replace(validResult, `docs/notes.md`, `/etc/passwd`, 1),
		"escaping output":  strings.Replace(validResult, `docs/notes.md`, `../x`, 1),
		"backslash output": strings.Replace(validResult, `docs/notes.md`, `docs\\notes.md`, 1),
		"empty output":     strings.Replace(validResult, `docs/notes.md`, ``, 1),
		"spaced ref":       strings.Replace(validResult, `refs/heads/task-1`, `refs/heads/a b`, 1),
		"missing task":     strings.Replace(validResult, `"task_id": "task-1",`, ``, 1),
		"duplicate key":    strings.Replace(validResult, `"status": "succeeded",`, `"status": "succeeded", "status": "failed",`, 1),
	}
	for name, data := range cases {
		_, err := ParseResult([]byte(data), testID)
		if !errors.Is(err, ErrMalformedResult) {
			t.Errorf("%s: ParseResult = %v, want ErrMalformedResult", name, err)
		}
	}
}

func TestParseResultRejectsAlternateKeyCase(t *testing.T) {
	fields := map[string]string{
		"schema": `"guildhall.artisan-result"`, "version": `1`,
		"task_id": `"task-1"`, "attempt_id": `"attempt-1"`,
		"status": `"succeeded"`, "summary": `"did the work"`,
		"outputs":    `[{"path": "docs/notes.md", "description": "notes"}]`,
		"landed_ref": `"refs/heads/task-1"`,
		"path":       `"docs/notes.md"`, "description": `"notes"`,
	}
	for key, value := range fields {
		for _, alias := range []string{strings.ToUpper(key), strings.ToUpper(key[:1]) + key[1:], strings.ReplaceAll(key, "s", "ſ")} {
			if alias == key {
				continue
			}
			canonical := `"` + key + `": ` + value
			alternate := `"` + alias + `": ` + value
			for _, replacement := range []string{alternate, canonical + ", " + alternate, alternate + ", " + canonical} {
				t.Run(key+"/"+replacement, func(t *testing.T) {
					data := strings.Replace(validResult, canonical, replacement, 1)
					if _, err := ParseResult([]byte(data), testID); !errors.Is(err, ErrMalformedResult) {
						t.Fatalf("ParseResult = %v, want ErrMalformedResult", err)
					}
				})
			}
		}
	}
	for _, data := range []string{
		strings.Replace(validResult, `"version": 1`, `"version": 2, "VERSION": 1`, 1),
		strings.Replace(validResult, `"status": "succeeded"`, `"status": "failed", "STATUS": "succeeded"`, 1),
	} {
		if _, err := ParseResult([]byte(data), testID); !errors.Is(err, ErrMalformedResult) {
			t.Errorf("conflicting fields: ParseResult = %v, want ErrMalformedResult", err)
		}
	}
}

func TestParseResultRejectsOtherIdentity(t *testing.T) {
	for _, id := range []Identity{{TaskID: "task-2", AttemptID: "attempt-1"}, {TaskID: "task-1", AttemptID: "attempt-2"}} {
		if _, err := ParseResult([]byte(validResult), id); !errors.Is(err, ErrIdentityMismatch) {
			t.Errorf("ParseResult for %+v = %v, want ErrIdentityMismatch", id, err)
		}
	}
}

func TestParseResultRefusesNewerVersion(t *testing.T) {
	newer := strings.Replace(validResult, `"version": 1`, `"version": 2`, 1)
	if _, err := ParseResult([]byte(newer), testID); !errors.Is(err, ErrUnsupportedResultVersion) {
		t.Errorf("ParseResult v2 = %v, want ErrUnsupportedResultVersion", err)
	}
}

func TestIdentityValidation(t *testing.T) {
	for _, id := range []Identity{{}, {TaskID: "t"}, {TaskID: "-t", AttemptID: "a"}, {TaskID: "t/x", AttemptID: "a"}, {TaskID: "t", AttemptID: strings.Repeat("a", 129)}} {
		if err := id.Validate(); err == nil {
			t.Errorf("Identity %+v accepted", id)
		}
	}
	if err := testID.Validate(); err != nil {
		t.Errorf("valid identity refused: %v", err)
	}
}

func TestWriteResultIsCreateOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	r := Result{Identity: testID, Status: StatusSucceeded, Summary: "ok"}
	if err := WriteResult(path, r); err != nil {
		t.Fatalf("WriteResult = %v", err)
	}
	got, err := ReadResult(path, testID)
	if err != nil {
		t.Fatalf("ReadResult = %v", err)
	}
	if got.Summary != "ok" || got.Status != StatusSucceeded || got.Outputs == nil {
		t.Errorf("round trip = %+v", got)
	}
	r.Summary = "second"
	if err := WriteResult(path, r); !errors.Is(err, ErrResultExists) {
		t.Errorf("second WriteResult = %v, want ErrResultExists", err)
	}
	if got, _ := ReadResult(path, testID); got.Summary != "ok" {
		t.Errorf("result was overwritten: %+v", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Errorf("directory holds %v (%v), want only the result", entries, err)
	}
}

func TestWriteResultValidatesFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if err := WriteResult(path, Result{Identity: testID, Status: "maybe", Summary: "x"}); !errors.Is(err, ErrMalformedResult) {
		t.Errorf("WriteResult(bad status) = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("invalid result was written: %v", err)
	}
}

func TestReadResultMissingAndOversized(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadResult(filepath.Join(dir, "none.json"), testID); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing result = %v, want fs.ErrNotExist", err)
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, []byte(strings.Replace(validResult, "did the work", strings.Repeat("x", MaxResultBytes), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadResult(big, testID); !errors.Is(err, ErrMalformedResult) {
		t.Errorf("oversized result = %v, want ErrMalformedResult", err)
	}
}
