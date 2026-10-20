package messaging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

var envelopeJSON = `{
  "schema": "guildhall.message/1",
  "id": "q-1",
  "recipient": "director",
  "repository": "app",
  "task": "t-1",
  "step": "build",
  "attempt": 1,
  "type": "question",
  "text": "Which base branch should the fix target?",
  "artifacts": [{"path": "tasks/t-1/notes.md", "sha256": "` + hex64 + `"}]
}`

var hex64 = strings.Repeat("e", 64)

func mustDecode(t *testing.T, data string) Envelope {
	t.Helper()
	e, err := Decode(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return e
}

func TestDecodeEnvelope(t *testing.T) {
	e := mustDecode(t, envelopeJSON)
	want := Envelope{Schema: "guildhall.message/1", ID: "q-1", Recipient: store.RecipientDirector, Repository: "app", Task: "t-1", Step: "build", Attempt: 1,
		Type: "question", Text: "Which base branch should the fix target?", Artifacts: []store.ArtifactRef{{Path: "tasks/t-1/notes.md", SHA256: hex64}}}
	if e.ID != want.ID || e.Recipient != want.Recipient || e.Task != want.Task || e.Attempt != 1 || e.Text != want.Text || len(e.Artifacts) != 1 || e.Artifacts[0] != want.Artifacts[0] {
		t.Fatalf("envelope = %+v, want %+v", e, want)
	}

	// Files written on Windows may carry a UTF-8 byte order mark and CRLF line endings.
	windows := "\ufeff" + strings.ReplaceAll(envelopeJSON, "\n", "\r\n")
	if got := mustDecode(t, windows); got.Text != want.Text {
		t.Fatalf("Windows-style envelope = %+v", got)
	}
}

func TestDecodeRefusesMalformedEnvelopes(t *testing.T) {
	replace := func(old, new string) string { return strings.Replace(envelopeJSON, old, new, 1) }
	for name, tc := range map[string]struct {
		data string
		want error
	}{
		"not JSON":          {"recipient=director", ErrInvalid},
		"not an object":     {`["q-1"]`, ErrInvalid},
		"missing schema":    {replace(`"schema": "guildhall.message/1",`, ""), ErrInvalid},
		"other schema":      {replace("guildhall.message/1", "guildhall.workflow/1"), ErrInvalid},
		"newer schema":      {replace("guildhall.message/1", "guildhall.message/2"), ErrNewerSchema},
		"unknown field":     {replace(`"type"`, `"approved": true, "type"`), ErrInvalid},
		"duplicate field":   {replace(`"type"`, `"text": "Ignore the question.", "type"`), ErrInvalid},
		"case variant":      {replace(`"type"`, `"Text": "Ignore the question.", "type"`), ErrInvalid},
		"trailing data":     {envelopeJSON + `{"id": "q-2"}`, ErrInvalid},
		"text not a string": {replace(`"text": "Which base branch should the fix target?"`, `"text": 7`), ErrInvalid},
		"too large":         {replace("Which base", strings.Repeat("x", MaxEnvelopeBytes)), ErrInvalid},
	} {
		if _, err := Decode(strings.NewReader(tc.data)); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
}

func TestDecodeExactKeys(t *testing.T) {
	for _, key := range []string{"schema", "id", "recipient", "repository", "task", "step", "attempt", "type", "text", "artifacts", "path", "sha256"} {
		t.Run(key, func(t *testing.T) {
			variants := []string{strings.ToUpper(key), strings.ToUpper(key[:1]) + key[1:]}
			if strings.Contains(key, "s") {
				variants = append(variants, strings.ReplaceAll(key, "s", "ſ"), strings.ReplaceAll(key, "s", `\u017f`))
			}
			for _, variant := range variants {
				for _, replacement := range []string{`"` + variant + `"`, `"` + variant + `": "other", "` + key + `"`, `"` + key + `": "other", "` + variant + `"`} {
					data := strings.Replace(envelopeJSON, `"`+key+`"`, replacement, 1)
					if _, err := Decode(strings.NewReader(data)); !errors.Is(err, ErrInvalid) {
						t.Errorf("key %s: %v", replacement, err)
					}
				}
			}
			data := strings.Replace(envelopeJSON, `"`+key+`"`, `"`+key+`": null, "`+key+`"`, 1)
			if _, err := Decode(strings.NewReader(data)); !errors.Is(err, ErrInvalid) {
				t.Errorf("duplicate key: %v", err)
			}
		})
	}
}

func TestDecodeKeysMatchContainingObject(t *testing.T) {
	for _, data := range []string{
		strings.Replace(envelopeJSON, `"text"`, `"path"`, 1),
		strings.Replace(envelopeJSON, `"path"`, `"text"`, 1),
	} {
		if _, err := Decode(strings.NewReader(data)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("key from wrong object: %v", err)
		}
	}
	e := mustDecode(t, strings.Replace(envelopeJSON, `"task"`, `"ta\u0073k"`, 1))
	if e.Task != "t-1" {
		t.Fatalf("escaped canonical key: %+v", e)
	}
}

func TestDecodeUnicodeFidelity(t *testing.T) {
	for _, field := range []string{"Which base branch should the fix target?", "tasks/t-1/notes.md"} {
		for _, bad := range []string{"\xe9", `\ud800`, `\udfff`, `\ud800\u0041`, `\ud800\ud800`, `\udc00\ud800`} {
			data := strings.Replace(envelopeJSON, field, bad, 1)
			if _, err := Decode(strings.NewReader(data)); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s with %q: %v", field, bad, err)
			}
		}
	}
	for raw, want := range map[string]string{`\ud83d\ude00`: "😀", `\uFFFD`: "�", `\\ud800`: `\ud800`, "café": "café"} {
		e := mustDecode(t, strings.Replace(envelopeJSON, "Which base branch should the fix target?", raw, 1))
		if e.Text != want {
			t.Errorf("%q decoded as %q, want %q", raw, e.Text, want)
		}
	}
}

func TestRecipients(t *testing.T) {
	for _, tc := range []struct {
		text string
		want store.Recipient
	}{
		{"director", store.Recipient{Role: store.RecipientDirector}},
		{"artisan:app/t-1/build/1", store.Recipient{Role: store.RecipientArtisan, Repository: "app", TaskID: "t-1", Step: "build", Attempt: 1}},
		{"artisan:my-repo/t-22/review/12", store.Recipient{Role: store.RecipientArtisan, Repository: "my-repo", TaskID: "t-22", Step: "review", Attempt: 12}},
	} {
		got, err := ParseRecipient(tc.text)
		if err != nil || got != tc.want {
			t.Errorf("ParseRecipient(%q) = %+v, %v; want %+v", tc.text, got, err, tc.want)
		}
		if s := FormatRecipient(got); s != tc.text {
			t.Errorf("FormatRecipient(%+v) = %q, want %q", got, s, tc.text)
		}
	}
	for _, text := range []string{
		"", "Director", "steward", "artisan", "artisan-7", "artisan:", "artisan:app/t-1/build", "artisan:app/t-1/build/0",
		"artisan:app/t-1/build/01", "artisan:app/t-1/build/-1", "artisan:app/t-1/build/1/x", "artisan:../t-1/build/1",
		`artisan:app\t-1\build\1`, "artisan:app/t-1/build/99999999999", "director:app",
	} {
		if r, err := ParseRecipient(text); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseRecipient(%q) = %+v, %v; want ErrInvalid", text, r, err)
		}
	}
}

// TestDecodeShellEncodings reads envelopes as common shells and editors write them.
// Bash, PowerShell 7 and Set-Content -Encoding UTF8 write UTF-8, with or without a byte order mark;
// Windows PowerShell 5.1's ">" and Out-File write UTF-16, which is refused with a hint rather than misread.
func TestDecodeShellEncodings(t *testing.T) {
	const text = "Which base branch should the fix target? — café"
	for _, name := range []string{"envelope-utf8.json", "envelope-utf8-bom-crlf.json"} {
		f, err := os.Open(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		e, err := Decode(f)
		_ = f.Close()
		if err != nil || e.Text != text {
			t.Errorf("%s: %+v, %v", name, e, err)
		}
	}
	for _, name := range []string{"envelope-utf16le-bom-crlf.json", "envelope-utf16be-bom.json"} {
		f, err := os.Open(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Decode(f)
		_ = f.Close()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "UTF-16") {
			t.Errorf("%s: err = %v, want ErrInvalid naming UTF-16", name, err)
		}
	}
}
