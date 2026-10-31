package config

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

var (
	// tableHeaderPattern is a standard or array table header line, with an optional comment.
	tableHeaderPattern = regexp.MustCompile(`^\s*(\[\[?)\s*([^\[\]]*?)\s*\]\]?\s*(#.*)?$`)
	// localKeyPattern is a [local] key line whose bare key and one-line string value SetLocal can replace.
	localKeyPattern = regexp.MustCompile(`^(\s*)([A-Za-z0-9_-]+)\s*=\s*(.*)$`)
	// topLocalPattern is a top-level local key, inline (local = {...}) or dotted (local.key = ...).
	topLocalPattern = regexp.MustCompile(`^\s*("local"|'local'|local)\s*[.=]`)
)

// SetLocal returns the config.toml text local with each key of values set under [local] to its value, as a
// targeted line edit that keeps every other byte: a key [local] already holds with another value has its line
// replaced, one that holds the value keeps its line, a new key is
// added after the table's last line, in key order, and a config with no local table gains a [local] table at its
// end. It refuses, changing nothing, a local table it cannot edit safely: an inline or dotted local table, a
// repeated or nested [local], a key that is quoted or dotted, or a value that is not a one-line string
// (docs/director-modes.md#enrol-and-leave). The caller re-parses the result to check it.
func SetLocal(local []byte, values map[string]string) ([]byte, error) {
	if len(values) == 0 {
		return local, nil
	}
	var raw map[string]any
	md, err := toml.Decode(string(local), &raw)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	newline := "\n"
	if bytes.Contains(local, []byte("\r\n")) {
		newline = "\r\n"
	}
	lines := strings.SplitAfter(string(local), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	header, last, table := -1, -1, ""
	set := map[string]bool{}
	for i, line := range lines {
		text := strings.TrimRight(line, "\r\n")
		if m := tableHeaderPattern.FindStringSubmatch(text); m != nil && strings.HasPrefix(strings.TrimSpace(text), "[") {
			table = m[2]
			if m[1] == "[[" {
				table = "[[" + table
			}
			switch {
			case table == "local" && header >= 0:
				return nil, errors.New("config.toml has more than one [local] table")
			case table == "local":
				header, last = i, i
			case strings.HasPrefix(table, "local.") || table == "[[local" || strings.HasPrefix(table, "[[local."):
				return nil, fmt.Errorf("config.toml has a nested or array local table [%s], which leave cannot edit", m[2])
			}
			continue
		}
		if table == "" && topLocalPattern.MatchString(text) {
			return nil, errors.New("config.toml defines local inline or with dotted keys; make it a [local] table first")
		}
		if table != "local" {
			continue
		}
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		last = i
		m := localKeyPattern.FindStringSubmatch(text)
		if m == nil {
			return nil, fmt.Errorf("config.toml [local] line %d is not a bare key and one-line string, which leave cannot edit", i+1)
		}
		value := strings.TrimSpace(m[3])
		if strings.HasPrefix(value, `"""`) || strings.HasPrefix(value, `'''`) || (!strings.HasPrefix(value, `"`) && !strings.HasPrefix(value, `'`)) {
			return nil, fmt.Errorf("config.toml [local] %s is not a one-line string, which leave cannot edit", m[2])
		}
		if v, ok := values[m[2]]; ok {
			// A line that already holds the value is kept as it is, comment and all.
			var held map[string]string
			if _, err := toml.Decode(text, &held); err != nil || held[m[2]] != v {
				lines[i] = m[1] + m[2] + " = " + tomlString(v) + newline
			}
			set[m[2]] = true
		}
	}
	if header < 0 && md.IsDefined("local") {
		return nil, errors.New("config.toml defines local inline or with dotted keys; make it a [local] table first")
	}
	var add []string
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if !set[k] {
			add = append(add, k+" = "+tomlString(values[k])+newline)
		}
	}
	if len(add) == 0 {
		return []byte(strings.Join(lines, "")), nil
	}
	if header < 0 {
		if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
			lines[n-1] += newline
		}
		if len(lines) > 0 {
			lines = append(lines, newline)
		}
		lines = append(lines, "[local]"+newline)
		lines = append(lines, add...)
		return []byte(strings.Join(lines, "")), nil
	}
	if !strings.HasSuffix(lines[last], "\n") {
		lines[last] += newline
	}
	out := slices.Concat(lines[:last+1], add, lines[last+1:])
	return []byte(strings.Join(out, "")), nil
}

// tomlString is v as a TOML basic string.
func tomlString(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
