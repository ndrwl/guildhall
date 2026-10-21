package digest_test

import (
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/digest"
)

const (
	emptyHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	abcHex   = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

func TestOf(t *testing.T) {
	for data, want := range map[string]digest.Digest{"": "sha256:" + emptyHex, "abc": "sha256:" + abcHex} {
		got := digest.Of([]byte(data))
		if got != want {
			t.Errorf("Of(%q) = %q, want %q", data, got, want)
		}
		if got.Hex() != strings.TrimPrefix(string(want), "sha256:") {
			t.Errorf("Of(%q).Hex() = %q", data, got.Hex())
		}
	}
}

// refused are spellings neither Parse nor ParseHex accepts, before or after a prefix.
var refused = map[string]string{
	"empty":       "",
	"short":       abcHex[:63],
	"long":        abcHex + "0",
	"uppercase":   strings.ToUpper(abcHex),
	"one upper":   "B" + abcHex[1:],
	"not hex":     "g" + abcHex[1:],
	"sha-1 size":  abcHex[:40],
	"leading sp":  " " + abcHex[1:],
	"trailing sp": abcHex[1:] + " ",
	"newline":     abcHex[1:] + "\n",
}

func TestParse(t *testing.T) {
	got, err := digest.Parse("sha256:" + abcHex)
	if err != nil || got != digest.Digest("sha256:"+abcHex) || got.Hex() != abcHex {
		t.Fatalf("Parse(valid) = %q, %v", got, err)
	}
	cases := map[string]string{
		"bare":            abcHex,
		"upper prefix":    "SHA256:" + abcHex,
		"other algorithm": "sha512:" + abcHex,
		"no colon":        "sha256" + abcHex,
		"double prefix":   "sha256:sha256:" + abcHex,
		"space":           " sha256:" + abcHex,
	}
	for name, s := range refused {
		cases["prefixed "+name] = "sha256:" + s
	}
	for name, s := range cases {
		if d, err := digest.Parse(s); err == nil {
			t.Errorf("%s: Parse(%q) = %q, want an error", name, s, d)
		} else if !strings.Contains(err.Error(), "sha256:<64 lowercase hex>") {
			t.Errorf("%s: error %q does not name the expected spelling", name, err)
		}
	}
}

func TestParseHex(t *testing.T) {
	got, err := digest.ParseHex(abcHex)
	if err != nil || got != digest.Digest("sha256:"+abcHex) || got.Hex() != abcHex {
		t.Fatalf("ParseHex(valid) = %q, %v", got, err)
	}
	cases := map[string]string{"prefixed": "sha256:" + abcHex}
	for name, s := range refused {
		cases[name] = s
	}
	for name, s := range cases {
		if d, err := digest.ParseHex(s); err == nil {
			t.Errorf("%s: ParseHex(%q) = %q, want an error", name, s, d)
		} else if !strings.Contains(err.Error(), "64 lowercase hex") {
			t.Errorf("%s: error %q does not name the expected spelling", name, err)
		}
	}
}

func TestSpellingsRoundTrip(t *testing.T) {
	d := digest.Of([]byte("guildhall"))
	bare, err := digest.ParseHex(d.Hex())
	if err != nil || bare != d {
		t.Fatalf("ParseHex(%q) = %q, %v, want %q", d.Hex(), bare, err, d)
	}
	prefixed, err := digest.Parse(string(d))
	if err != nil || prefixed != d {
		t.Fatalf("Parse(%q) = %q, %v", d, prefixed, err)
	}
}

func TestValidHead(t *testing.T) {
	for _, s := range []string{abcHex[:40], abcHex} {
		if !digest.ValidHead(s) {
			t.Errorf("ValidHead(%q) = false, want true", s)
		}
	}
	cases := map[string]string{"prefixed": "sha256:" + abcHex, "41": abcHex[:41], "39": abcHex[:39]}
	for name, s := range refused {
		if s != abcHex[:40] {
			cases[name] = s
		}
	}
	cases["sha-1 upper"] = strings.ToUpper(abcHex[:40])
	for name, s := range cases {
		if digest.ValidHead(s) {
			t.Errorf("%s: ValidHead(%q) = true, want false", name, s)
		}
	}
}
