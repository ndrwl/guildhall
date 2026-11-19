package spec

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrSummaryItem reports a Summary item Guildhall reads, Size or Touches, that is missing or not in its strict form.
// Intake never refuses a spec for it: it only makes the plan ineligible for Director acceptance, with the reason.
var ErrSummaryItem = errors.New("summary item unreadable")

// The Summary items Guildhall reads (SummarySize, SummaryTouches): each a list item starting with its bold label.
const (
	SizeItem    = "- **Size:**"
	TouchesItem = "- **Touches:**"
)

// The sensitive areas a plan's Touches item may name. A plan that names any is never accepted by the Director.
const (
	TouchSecrets         = "secrets"
	TouchManagedSettings = "managed settings"
	TouchTrust           = "trust"
	// TouchNone is the whole Touches item of a plan that names no sensitive area.
	TouchNone = "none"
)

// Touches are the sensitive areas, in documented order.
var Touches = []string{TouchSecrets, TouchManagedSettings, TouchTrust}

// Size is a plan's stated size, from its Summary's Size item.
type Size struct {
	// Slices is the stated slice count, 0 when the item does not state one.
	Slices int
	// Lines and Files are the stated non-test lines and files.
	Lines, Files int
}

// count is a whole number with optional thousands separators, such as 1,150.
const count = `([0-9]{1,3}(?:,[0-9]{3})+|[0-9]+)`

// sizePattern is the Size item's text after its label.
var sizePattern = regexp.MustCompile(`^(?:` + count + ` slices?, )?about ` + count + ` non-test lines? in ` + count + ` files?\.?$`)

// SizeForm and TouchesForm are the strict forms of the two items, as the errors and the plan guide quote them.
const (
	SizeForm    = "- **Size:** about <N> non-test lines in <M> files, optionally after `<k> slices, `"
	TouchesForm = "- **Touches:** none, or a list drawn from secrets, managed settings and trust"
)

// SummarySize reads the Size item of data's Summary block (Summary): `- **Size:** about <N> non-test lines in <M>
// files`, optionally after `<k> slices, `, with an optional ending full stop, and numbers with or without thousands
// separators. A spec with no Summary block, no Size item, two of them or one in another form is ErrSummaryItem,
// saying which. Parsing is a pure function of the bytes.
func SummarySize(data []byte) (Size, error) {
	text, err := summaryItem(data, SizeItem, SizeForm)
	if err != nil {
		return Size{}, err
	}
	m := sizePattern.FindStringSubmatch(text)
	if m == nil {
		return Size{}, fmt.Errorf("%w: %s %q is not in the form %s", ErrSummaryItem, SizeItem, text, SizeForm)
	}
	var s Size
	for i, p := range []*int{&s.Slices, &s.Lines, &s.Files} {
		if m[i+1] == "" {
			continue
		}
		n, err := strconv.Atoi(strings.ReplaceAll(m[i+1], ",", ""))
		if err != nil {
			return Size{}, fmt.Errorf("%w: %s %q: %v", ErrSummaryItem, SizeItem, text, err)
		}
		*p = n
	}
	return s, nil
}

// SummaryTouches reads the Touches item of data's Summary block: `- **Touches:** none`, which returns an empty,
// non-nil list, or a list of the sensitive areas (Touches) separated by commas or "and", in any order, each once,
// with an optional ending full stop; case is ignored. A spec with no Summary block, no Touches item, two of them or
// one naming anything else is ErrSummaryItem, saying which. Parsing is a pure function of the bytes.
func SummaryTouches(data []byte) ([]string, error) {
	text, err := summaryItem(data, TouchesItem, TouchesForm)
	if err != nil {
		return nil, err
	}
	body := strings.ToLower(strings.TrimSuffix(text, "."))
	if body == TouchNone {
		return []string{}, nil
	}
	var areas []string
	for part := range strings.SplitSeq(strings.ReplaceAll(body, " and ", ","), ",") {
		part = strings.TrimSpace(part)
		switch {
		case !slices.Contains(Touches, part):
			return nil, fmt.Errorf("%w: %s %q names %q; write %s", ErrSummaryItem, TouchesItem, text, part, TouchesForm)
		case slices.Contains(areas, part):
			return nil, fmt.Errorf("%w: %s %q names %q twice", ErrSummaryItem, TouchesItem, text, part)
		}
		areas = append(areas, part)
	}
	return areas, nil
}

// summaryItem returns the text after label of the one Summary line starting with it, trimmed.
func summaryItem(data []byte, label, form string) (string, error) {
	block, ok := Summary(data)
	if !ok {
		return "", fmt.Errorf("%w: the spec has no usable %s block, so it has no %s item", ErrSummaryItem, SummaryHeading, label)
	}
	var found []string
	for l := range strings.SplitSeq(block, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), label); ok {
			found = append(found, strings.TrimSpace(rest))
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("%w: the %s block has no %s item; write %s", ErrSummaryItem, SummaryHeading, label, form)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("%w: the %s block has %d %s items; write one", ErrSummaryItem, SummaryHeading, len(found), label)
}
