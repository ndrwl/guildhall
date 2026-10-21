// Package strictjson decodes one JSON document exactly as its Go type declares it.
//
// It is the shared reader for Guildhall's versioned JSON formats: files and
// standard input written by people and shells, and lines from other Guildhall
// processes. Each format keeps its own schema name, size limit and error
// vocabulary; this package only refuses what none of them may guess at.
// See docs/contracts/strictjson.md.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrNewerSchema reports a document whose schema version is newer than the
// caller supports; it is refused rather than guessed at.
var ErrNewerSchema = errors.New("schema is newer than this Guildhall supports")

// Options are one format's rules.
type Options struct {
	// Schema is the schema prefix, such as "guildhall.message/". When set, the
	// document must be an object whose top-level "schema" is Schema followed
	// by a version number written without sign or leading zeros, at least 1.
	// Empty disables schema-version validation; fields still follow the target type.
	Schema string
	// Version is the newest schema version the caller supports; a newer one is
	// ErrNewerSchema. It is ignored when Schema is empty.
	Version int
	// MaxBytes bounds the document, a byte order mark included; zero means no bound.
	MaxBytes int
}

// Decode decodes data, one JSON document, into v, which must be a non-nil
// pointer; what v points to is replaced only when the whole document is accepted.
//
// It refuses, before v is changed:
//   - a document longer than MaxBytes;
//   - UTF-16, with a hint to write UTF-8, because Windows PowerShell 5.1
//     writes it for ">" and Out-File;
//   - invalid UTF-8, invalid JSON, an unpaired surrogate escape, and data
//     after the document;
//   - a duplicate key in any object, at any depth;
//   - a key that v's type does not declare, compared exactly with the field's
//     json name: struct fields are matched by tag or field name, case
//     included, map values and slice and array elements are checked against
//     their element type, and a value whose type is an interface or decodes
//     itself is checked for duplicates only; embedded structs are not
//     promoted, so their fields are refused unless the embedding is tagged;
//   - a schema that is missing or not Schema<version>, and, as ErrNewerSchema,
//     a version above Version. A newer schema is reported even when the
//     document also has keys or shapes this version does not know.
//
// A leading UTF-8 byte order mark is dropped. Escaped keys are compared after
// unescaping. Every error other than ErrNewerSchema means the document is
// malformed for v's type; the message says why and callers add their own
// sentinel and context.
func Decode(data []byte, v any, opts Options) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return fmt.Errorf("decode target %T is not a non-nil pointer", v)
	}
	if opts.MaxBytes > 0 && len(data) > opts.MaxBytes {
		return fmt.Errorf("document is larger than %d bytes", opts.MaxBytes)
	}
	// Windows PowerShell 5.1 writes UTF-16 for ">" and Out-File; say so rather than report a JSON syntax error.
	if bytes.HasPrefix(data, []byte{0xff, 0xfe}) || bytes.HasPrefix(data, []byte{0xfe, 0xff}) {
		return errors.New("document is UTF-16; write it as UTF-8, for example with Set-Content -Encoding utf8")
	}
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if !utf8.Valid(data) {
		return errors.New("document is not valid UTF-8")
	}
	if !json.Valid(data) {
		return errors.New("document is not one JSON value")
	}
	if err := checkSurrogates(data); err != nil {
		return err
	}
	w := walker{dec: json.NewDecoder(bytes.NewReader(data)), schema: opts.Schema != ""}
	if err := w.value(rv.Type(), true); err != nil {
		return err
	}
	if opts.Schema != "" {
		if err := checkSchema(w, opts); err != nil {
			return err
		}
	}
	if w.shape != nil {
		return w.shape
	}
	fresh := reflect.New(rv.Type().Elem())
	if err := json.Unmarshal(data, fresh.Interface()); err != nil {
		return err
	}
	rv.Elem().Set(fresh.Elem())
	return nil
}

func checkSchema(w walker, opts Options) error {
	if !w.found {
		return fmt.Errorf("schema is missing; want %s<version>", opts.Schema)
	}
	num, ok := strings.CutPrefix(w.text, opts.Schema)
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n < 1 || strconv.Itoa(n) != num {
		return fmt.Errorf("schema %q is not %s<version>", w.text, opts.Schema)
	}
	if n > opts.Version {
		return fmt.Errorf("%w: %q (supported: %s%d)", ErrNewerSchema, w.text, opts.Schema, opts.Version)
	}
	return nil
}

// checkSurrogates refuses a \u escape of half a surrogate pair, which
// encoding/json would silently replace with U+FFFD. data is valid JSON.
func checkSurrogates(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if n < 0xd800 || n > 0xdfff {
			continue
		}
		if n > 0xdbff || i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return errors.New("unpaired Unicode surrogate")
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return errors.New("unpaired Unicode surrogate")
		}
		i += 6
	}
	return nil
}

// walker checks a document's keys against a type, token by token.
// A duplicate key ends the walk; a key or shape the type does not declare is
// kept in shape and the walk goes on, so a newer schema is still found.
type walker struct {
	dec *json.Decoder
	// schema asks for the top-level "schema" string, kept in text.
	schema bool
	found  bool
	text   string
	shape  error
}

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

// target returns the type whose keys a value of type t is checked against,
// or nil when only duplicates are checked.
func target(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer && !t.Implements(unmarshaler) {
		t = t.Elem()
	}
	if t == nil || t.Kind() == reflect.Interface || t.Implements(unmarshaler) || reflect.PointerTo(t).Implements(unmarshaler) {
		return nil
	}
	return t
}

func (w *walker) mismatch(format string, args ...any) {
	if w.shape == nil {
		w.shape = fmt.Errorf(format, args...)
	}
}

func (w *walker) value(t reflect.Type, top bool) error {
	t = target(t)
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		return w.object(t, top)
	case json.Delim('['):
		var elem reflect.Type
		if t != nil {
			if k := t.Kind(); k == reflect.Slice || k == reflect.Array {
				elem = t.Elem()
			} else {
				w.mismatch("unexpected array for %s", t)
			}
		}
		for w.dec.More() {
			if err := w.value(elem, false); err != nil {
				return err
			}
		}
		_, err = w.dec.Token()
	}
	return err
}

func (w *walker) object(t reflect.Type, top bool) error {
	if t != nil && t.Kind() != reflect.Struct && t.Kind() != reflect.Map {
		w.mismatch("unexpected object for %s", t)
		t = nil
	}
	seen := map[string]bool{}
	for w.dec.More() {
		tok, err := w.dec.Token()
		if err != nil {
			return err
		}
		key := tok.(string)
		if seen[key] {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		var elem reflect.Type
		switch {
		case t == nil:
		case t.Kind() == reflect.Map:
			elem = t.Elem()
		default:
			var ok bool
			if elem, ok = field(t, key); !ok {
				w.mismatch("unknown field %q", key)
			}
		}
		if top && w.schema && key == "schema" {
			var s *string
			if err := w.dec.Decode(&s); err != nil || s == nil {
				return errors.New("schema is not a string")
			}
			w.found, w.text = true, *s
			continue
		}
		if err := w.value(elem, false); err != nil {
			return err
		}
	}
	_, err := w.dec.Token()
	return err
}

// field returns the type of struct t's field whose json name is exactly key.
func field(t reflect.Type, key string) (reflect.Type, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if !f.IsExported() || f.Anonymous && tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name
		}
		if name == key {
			return f.Type, true
		}
	}
	return nil, false
}
