package strictjson_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/strictjson"
)

type item struct {
	Path string `json:"path"`
}

type doc struct {
	Schema string          `json:"schema"`
	Name   string          `json:"name"`
	Count  int             `json:"count,omitempty"`
	Items  []item          `json:"items,omitempty"`
	Tags   map[string]item `json:"tags,omitempty"`
	Next   *item           `json:"next,omitempty"`
	Extra  json.RawMessage `json:"extra,omitempty"`
	Any    any             `json:"any,omitempty"`
	Pair   [1]item         `json:"pair,omitempty"`
	Skip   string          `json:"-"`
	Plain  string
	Labels map[string]string `json:"labels,omitempty"`
}

var opts = strictjson.Options{Schema: "test.doc/", Version: 2}

const valid = `{"schema": "test.doc/1", "name": "n", "items": [{"path": "a"}], "tags": {"k": {"path": "b"}}, "next": {"path": "c"}, "pair": [{"path": "d"}]}`

func decode(data string, o strictjson.Options) (doc, error) {
	var d doc
	err := strictjson.Decode([]byte(data), &d, o)
	return d, err
}

func refuse(t *testing.T, name, data string, o strictjson.Options) {
	t.Helper()
	if d, err := decode(data, o); err == nil || errors.Is(err, strictjson.ErrNewerSchema) {
		t.Errorf("%s: Decode = %+v, %v; want a malformed-document error", name, d, err)
	}
}

func TestDecodeValid(t *testing.T) {
	d, err := decode(valid, opts)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "n" || len(d.Items) != 1 || d.Items[0].Path != "a" || d.Tags["k"].Path != "b" || d.Next.Path != "c" || d.Pair[0].Path != "d" {
		t.Fatalf("decoded %+v", d)
	}
	for name, data := range map[string]string{
		"older version":   strings.Replace(valid, "test.doc/1", "test.doc/2", 1),
		"go field name":   `{"schema": "test.doc/1", "Plain": "p"}`,
		"escaped key":     `{"schema": "test.doc/1", "name": "n"}`,
		"raw message":     `{"schema": "test.doc/1", "extra": {"anything": [1, {"x": 2}]}}`,
		"interface value": `{"schema": "test.doc/1", "any": {"anything": true}}`,
		"null values":     `{"schema": "test.doc/1", "items": null, "next": null}`,
	} {
		if _, err := decode(data, opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeMaxBytes(t *testing.T) {
	data := `{"name": "n"}`
	for _, tc := range []struct {
		max int
		ok  bool
	}{{0, true}, {len(data), true}, {len(data) - 1, false}} {
		_, err := decode(data, strictjson.Options{MaxBytes: tc.max})
		if (err == nil) != tc.ok {
			t.Errorf("MaxBytes %d: %v", tc.max, err)
		}
	}
	// The byte order mark counts towards the bound.
	refuse(t, "bom over bound", "\ufeff"+data, strictjson.Options{MaxBytes: len(data)})
}

func TestDecodeEncodings(t *testing.T) {
	for name, data := range map[string]string{
		"bom":       "\ufeff" + valid,
		"crlf":      strings.ReplaceAll("{\n\"schema\": \"test.doc/1\",\n\"name\": \"n\"\n}\n", "\n", "\r\n"),
		"bom crlf":  "\ufeff{\r\n\"schema\": \"test.doc/1\"\r\n}\r\n",
		"non-ascii": `{"schema": "test.doc/1", "name": "café 😀"}`,
	} {
		if _, err := decode(data, opts); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, data := range map[string]string{
		"utf-16le": "\xff\xfe{\x00}\x00",
		"utf-16be": "\xfe\xff\x00{\x00}",
	} {
		if _, err := decode(data, strictjson.Options{}); err == nil || !strings.Contains(err.Error(), "UTF-16") || !strings.Contains(err.Error(), "Set-Content -Encoding utf8") {
			t.Errorf("%s: %v, want a UTF-16 refusal with the PowerShell hint", name, err)
		}
	}
	refuse(t, "invalid utf-8", `{"schema": "test.doc/1", "name": "`+"\xe9"+`"}`, opts)
	refuse(t, "second bom", "\ufeff\ufeff"+valid, opts)
}

// TestDecodeShellFixtures reads the messaging envelopes as shells and editors write them.
func TestDecodeShellFixtures(t *testing.T) {
	type envelope struct {
		Schema     string `json:"schema"`
		ID         string `json:"id"`
		Recipient  string `json:"recipient"`
		Repository string `json:"repository"`
		Task       string `json:"task"`
		Type       string `json:"type"`
		Text       string `json:"text"`
	}
	o := strictjson.Options{Schema: "guildhall.message/", Version: 1, MaxBytes: 1 << 10}
	for name, ok := range map[string]bool{
		"envelope-utf8.json":             true,
		"envelope-utf8-bom-crlf.json":    true,
		"envelope-utf16le-bom-crlf.json": false,
		"envelope-utf16be-bom.json":      false,
	} {
		data, err := os.ReadFile(filepath.Join("..", "messaging", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var e envelope
		err = strictjson.Decode(data, &e, o)
		if ok && (err != nil || e.Text != "Which base branch should the fix target? — café") {
			t.Errorf("%s: %+v, %v", name, e, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "UTF-16")) {
			t.Errorf("%s: %v, want a UTF-16 refusal", name, err)
		}
	}
}

func TestDecodeSurrogates(t *testing.T) {
	for _, bad := range []string{`\ud800`, `\udfff`, `\ud800A`, `\ud800\ud800`, `\udc00\ud800`, `\ud800x`} {
		refuse(t, bad, `{"schema": "test.doc/1", "name": "`+bad+`"}`, opts)
	}
	for raw, want := range map[string]string{`😀`: "😀", `�`: "�", `\\ud800`: `\ud800`, `a\\😀`: `a\😀`} {
		d, err := decode(`{"schema": "test.doc/1", "name": "`+raw+`"}`, opts)
		if err != nil || d.Name != want {
			t.Errorf("%s: %q, %v; want %q", raw, d.Name, err, want)
		}
	}
}

func TestDecodeDuplicateKeys(t *testing.T) {
	for name, data := range map[string]string{
		"top level":     `{"schema": "test.doc/1", "name": "a", "name": "b"}`,
		"schema":        `{"schema": "test.doc/1", "schema": "test.doc/1"}`,
		"escaped":       `{"schema": "test.doc/1", "name": "a", "name": "b"}`,
		"in slice":      `{"schema": "test.doc/1", "items": [{"path": "a", "path": "b"}]}`,
		"in map value":  `{"schema": "test.doc/1", "tags": {"k": {"path": "a", "path": "b"}}}`,
		"map key":       `{"schema": "test.doc/1", "labels": {"k": "a", "k": "b"}}`,
		"in pointer":    `{"schema": "test.doc/1", "next": {"path": "a", "path": "b"}}`,
		"raw message":   `{"schema": "test.doc/1", "extra": {"x": 1, "x": 2}}`,
		"interface":     `{"schema": "test.doc/1", "any": [{"x": 1, "x": 2}]}`,
		"newer version": `{"schema": "test.doc/3", "name": "a", "name": "b"}`,
	} {
		refuse(t, name, data, opts)
	}
}

func TestDecodeUnknownFields(t *testing.T) {
	for name, data := range map[string]string{
		"top level":       `{"schema": "test.doc/1", "approved": true}`,
		"case variant":    `{"schema": "test.doc/1", "Name": "n"}`,
		"upper case":      `{"schema": "test.doc/1", "NAME": "n"}`,
		"long s":          `{"schema": "test.doc/1", "itemſ": []}`,
		"long s escaped":  `{"schema": "test.doc/1", "itemſ": []}`,
		"go name tagged":  `{"schema": "test.doc/1", "Name": "n", "name": "n"}`,
		"dash field":      `{"schema": "test.doc/1", "Skip": "s"}`,
		"in slice":        `{"schema": "test.doc/1", "items": [{"path": "a", "mode": 1}]}`,
		"in map value":    `{"schema": "test.doc/1", "tags": {"k": {"Path": "a"}}}`,
		"in pointer":      `{"schema": "test.doc/1", "next": {"path": "a", "x": 1}}`,
		"in array":        `{"schema": "test.doc/1", "pair": [{"x": 1}]}`,
		"key of sibling":  `{"schema": "test.doc/1", "items": [{"name": "a"}]}`,
		"object for list": `{"schema": "test.doc/1", "items": {"path": "a"}}`,
		"list for object": `{"schema": "test.doc/1", "next": [{"path": "a"}]}`,
		"wrong type":      `{"schema": "test.doc/1", "count": "7"}`,
		"not an object":   `["test.doc/1"]`,
	} {
		refuse(t, name, data, opts)
	}
}

func TestDecodeTrailingData(t *testing.T) {
	for name, data := range map[string]string{
		"second document": valid + ` {"name": "m"}`,
		"garbage":         valid + `x`,
		"empty":           ``,
		"whitespace only": " \r\n",
		"not json":        `name=n`,
		"truncated":       valid[:len(valid)-1],
	} {
		refuse(t, name, data, opts)
	}
	if _, err := decode(valid+" \r\n\t", opts); err != nil {
		t.Errorf("trailing whitespace: %v", err)
	}
}

func TestDecodeSchema(t *testing.T) {
	for name, schema := range map[string]string{
		"other format":   `"other.doc/1"`,
		"no version":     `"test.doc/"`,
		"zero":           `"test.doc/0"`,
		"negative":       `"test.doc/-1"`,
		"signed":         `"test.doc/+1"`,
		"leading zero":   `"test.doc/01"`,
		"not a number":   `"test.doc/one"`,
		"suffix":         `"test.doc/1.0"`,
		"not a string":   `1`,
		"null":           `null`,
		"case variant":   `"Test.doc/1"`,
		"overflow":       `"test.doc/99999999999999999999"`,
		"surrounding ws": `" test.doc/1"`,
	} {
		refuse(t, name, `{"schema": `+schema+`, "name": "n"}`, opts)
	}
	refuse(t, "missing", `{"name": "n"}`, opts)
	refuse(t, "nested only", `{"next": {"schema": "test.doc/1"}}`, opts)

	for name, data := range map[string]string{
		"newer":                `{"schema": "test.doc/3", "name": "n"}`,
		"newer with new field": `{"schema": "test.doc/3", "name": "n", "colour": "red"}`,
		"newer with new shape": `{"schema": "test.doc/3", "items": {"path": "a"}, "next": [1]}`,
		"newer with new types": `{"schema": "test.doc/3", "count": "many"}`,
		"far future":           `{"schema": "test.doc/99", "op": "cancel"}`,
	} {
		d, err := decode(data, opts)
		if !errors.Is(err, strictjson.ErrNewerSchema) {
			t.Errorf("%s: %v, want ErrNewerSchema", name, err)
		}
		if d.Name != "" || d.Schema != "" {
			t.Errorf("%s: target changed to %+v", name, d)
		}
	}

	// Without a schema the field is an ordinary one: no prefix or version is required.
	if _, err := decode(`{"name": "n"}`, strictjson.Options{}); err != nil {
		t.Errorf("no schema option: %v", err)
	}
	if _, err := decode(`{"schema": "anything", "name": "n"}`, strictjson.Options{}); err != nil {
		t.Errorf("no schema option, schema field: %v", err)
	}
}

func TestDecodeLeavesTargetOnRefusal(t *testing.T) {
	d := doc{Name: "kept"}
	if err := strictjson.Decode([]byte(`{"schema": "test.doc/1", "name": "new", "x": 1}`), &d, opts); err == nil || d.Name != "kept" {
		t.Fatalf("Decode = %v, target %+v; want a refusal leaving the target unchanged", err, d)
	}
}

func TestDecodeNonObjectTargets(t *testing.T) {
	var list []item
	if err := strictjson.Decode([]byte(`[{"path": "a"}, {"path": "b"}]`), &list, strictjson.Options{}); err != nil || len(list) != 2 {
		t.Errorf("list: %+v, %v", list, err)
	}
	if err := strictjson.Decode([]byte(`[{"path": "a", "x": 1}]`), &list, strictjson.Options{}); err == nil {
		t.Error("unknown field in a list element was accepted")
	}
	var m map[string]int
	if err := strictjson.Decode([]byte(`{"a": 1, "a": 2}`), &m, strictjson.Options{}); err == nil {
		t.Error("duplicate map key was accepted")
	}
	var s string
	if err := strictjson.Decode([]byte(`"x"`), &s, strictjson.Options{}); err != nil || s != "x" {
		t.Errorf("string: %q, %v", s, err)
	}
	if err := strictjson.Decode([]byte(`{}`), item{}, strictjson.Options{}); err == nil {
		t.Error("non-pointer target was accepted")
	}
}
