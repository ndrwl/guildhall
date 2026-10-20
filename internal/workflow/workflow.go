// Package workflow parses and validates Guildhall workflow definitions.
//
// A workflow is a JSON file naming the built-in step kinds of one task in order,
// with typed input wiring and bounded loop rules.
// Parsing and validation are pure: they read no files, run nothing and grant no authority.
// Deciding who may author, approve or activate a workflow edit is deliberately outside this package.
package workflow

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// SchemaPrefix and SchemaVersion form the workflow "schema" value, for example "guildhall.workflow/1".
const (
	SchemaPrefix  = "guildhall.workflow/"
	SchemaVersion = 1
)

// MaxLoopRounds caps the "max" of any loop rule so every loop is bounded.
const MaxLoopRounds = 10

// ErrNewerSchema reports a document written for a newer Guildhall; it is refused rather than guessed at.
var ErrNewerSchema = errors.New("schema is newer than this Guildhall supports")

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ValidID reports whether s is a valid workflow or step identifier.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Definition is one parsed workflow.
type Definition struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Steps       []Step `json:"steps"`
}

// Step is one use of a built-in kind.
type Step struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Version  int                        `json:"version"`
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Inputs maps each input port to "step.output" references.
	Inputs map[string]Refs `json:"inputs,omitempty"`
	// On maps an outcome to a loop rule.
	On map[string]Rule `json:"on,omitempty"`
}

// Rule sends the task back to the same or an earlier step on an outcome, at most Max times.
type Rule struct {
	Goto string `json:"goto"`
	Max  int    `json:"max"`
}

type Refs []string

// Revision identifies the exact workflow content a task binds when accepted.
type Revision struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

//go:embed default.json
var defaultJSON []byte

// Default returns Guildhall's shipped default workflow.
func Default() (*Definition, error) {
	def, err := Parse(defaultJSON)
	if err != nil {
		return nil, err
	}
	return def, Validate(def)
}

// Parse decodes a workflow strictly: unknown fields, duplicate keys, trailing data
// and unknown or newer schemas are refused. It does not validate wiring; call Validate.
func Parse(data []byte) (*Definition, error) {
	if err := checkSchema(data, SchemaPrefix, SchemaVersion); err != nil {
		return nil, err
	}
	var def Definition
	if err := decodeStrict(data, &def); err != nil {
		return nil, fmt.Errorf("workflow: %w", err)
	}
	return &def, nil
}

// checkSchema reads only the "schema" field so a newer document is refused before strict decoding.
func checkSchema(data []byte, prefix string, supported int) error {
	var head map[string]json.RawMessage
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("malformed JSON: %w", err)
	}
	var schema string
	if err := json.Unmarshal(head["schema"], &schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	num, ok := strings.CutPrefix(schema, prefix)
	version, err := strconv.Atoi(num)
	if !ok || err != nil || version < 1 {
		return fmt.Errorf("schema %q is not %s<version>", schema, prefix)
	}
	if version > supported {
		return fmt.Errorf("%w: %q (supported: %s%d)", ErrNewerSchema, schema, prefix, supported)
	}
	return nil
}

func decodeStrict(data []byte, v any) error {
	if err := checkJSONKeys(json.NewDecoder(bytes.NewReader(data)), reflect.TypeOf(v).Elem()); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

func checkJSONKeys(dec *json.Decoder, typ reflect.Type) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name := key.(string)
			if seen[name] {
				return fmt.Errorf("duplicate key %q", name)
			}
			seen[name] = true
			var child reflect.Type
			if typ != nil {
				switch typ.Kind() {
				case reflect.Struct:
					for i := 0; i < typ.NumField(); i++ {
						field := typ.Field(i)
						tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
						if tag == name {
							child = field.Type
							break
						}
					}
					if child == nil {
						return fmt.Errorf("unknown field %q", name)
					}
				case reflect.Map:
					child = typ.Elem()
				}
			}
			if err := checkJSONKeys(dec, child); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	case json.Delim('['):
		var child reflect.Type
		if typ != nil && typ.Kind() == reflect.Slice {
			child = typ.Elem()
		}
		for dec.More() {
			if err := checkJSONKeys(dec, child); err != nil {
				return err
			}
		}
		_, err = dec.Token()
	}
	return err
}

// Digest is the sha256 of the definition's canonical JSON encoding, so formatting does not change it.
func (d *Definition) Digest() string {
	data, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("workflow: canonical encoding failed: %v", err))
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Revision returns the immutable revision a task binds.
func (d *Definition) Revision() Revision {
	return Revision{Name: d.Name, Digest: d.Digest()}
}

// Override replaces a review step's kind for one repository, for example automated-review with human-review.
type Override struct {
	Type    string `toml:"type" json:"type"`
	Version int    `toml:"version" json:"version"`
}

// ApplyOverrides returns a copy of def with the given steps' kinds replaced.
// Only review kinds can be swapped, and only for other review kinds, so the wiring stays the same.
// The result has its own digest; validate it before use.
func ApplyOverrides(def *Definition, overrides map[string]Override) (*Definition, error) {
	out := *def
	out.Steps = append([]Step(nil), def.Steps...)
	for id, ov := range overrides {
		i := out.stepIndex(id)
		if i < 0 {
			return nil, fmt.Errorf("override: unknown step %q", id)
		}
		from, ok := LookupKind(out.Steps[i].Type, out.Steps[i].Version)
		if !ok || !from.Review {
			return nil, fmt.Errorf("override %q: only review steps can be overridden", id)
		}
		to, err := lookup(ov.Type, ov.Version)
		if err != nil {
			return nil, fmt.Errorf("override %q: %w", id, err)
		}
		if !to.Review {
			return nil, fmt.Errorf("override %q: only review steps can replace a review step", id)
		}
		out.Steps[i].Type, out.Steps[i].Version = ov.Type, ov.Version
	}
	return &out, nil
}

func lookup(typ string, version int) (Kind, error) {
	if k, ok := LookupKind(typ, version); ok {
		return k, nil
	}
	if knownType(typ) {
		return Kind{}, fmt.Errorf("step type %q does not support version %d", typ, version)
	}
	return Kind{}, fmt.Errorf("unknown step type %q", typ)
}

func (d *Definition) stepIndex(id string) int {
	for i, s := range d.Steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}
