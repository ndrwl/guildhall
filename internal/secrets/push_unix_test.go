//go:build unix

package secrets_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/secrets"
)

// RoundTripSops is a fake sops that encrypts and decrypts for real enough to test with: --encrypt reads the flat JSON
// on its standard input and writes each value base64-encoded as ENC[...] with the age recipients it was given in
// sops metadata, and --decrypt reverses it when the key file's "# public key:" line is one of them. Values must hold
// no '"', ',', '{' or '}'.
const roundTripSops = `case "$1" in
--encrypt)
  [ "$2 $4 $5 $6 $7 $8" = "--age --input-type json --output-type yaml /dev/stdin" ] || exit 64
  { tr -d '{}\n' | tr ',' '\n'; echo; } | while IFS= read -r kv; do
    [ -n "$kv" ] || continue
    name=${kv%%\":*}; name=${name#\"}
    val=${kv#*\":\"}; val=${val%\"}
    printf '%s: ENC[AES256_GCM,data:%s,type:str]\n' "$name" "$(printf %s "$val" | base64 | tr -d '\n')"
  done
  echo 'sops:'
  echo '    age:'
  for r in $(echo "$3" | tr ',' ' '); do
    echo "        - recipient: $r"
    echo '          enc: |'
    echo '            -----BEGIN AGE ENCRYPTED FILE-----'
  done
  echo '    version: 3.9.0'
  ;;
--decrypt)
  [ "$2 $3" = "--output-type json" ] && [ -f "$SOPS_AGE_KEY_FILE" ] || exit 64
  pub=$(sed -n 's/^# public key: //p' "$SOPS_AGE_KEY_FILE")
  grep -q "recipient: $pub\$" "$4" || { echo 'Failed to get the data key required to decrypt the SOPS file.' >&2; exit 128; }
  printf '{'
  grep -E '^[A-Za-z_][A-Za-z0-9_]*: ENC\[' "$4" | {
    sep=''
    while IFS= read -r line; do
      name=${line%%:*}; data=${line#*data:}; data=${data%%,type*}
      printf '%s"%s":"%s"' "$sep" "$name" "$(printf %s "$data" | base64 -d)"
      sep=','
    done
  }
  printf '}\n'
  ;;
*) exit 64 ;;
esac`

func roundTripTools(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	scripts := map[string]string{
		"sops":       roundTripSops,
		"age-keygen": `[ "$1" = -y ] && sed -n 's/^# public key: //p' "$2"`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

const (
	machinePub  = "age1machine0000000000000000000000000000000000000000000000000"
	recoveryPub = "age1recovery000000000000000000000000000000000000000000000000"
)

// TestEncryptCheckRoundTrip: Encrypt writes ciphertext holding the names and recipients and no value, which Check
// reports decrypting with the machine's key to those names, and which a key it is not encrypted to cannot decrypt.
func TestEncryptCheckRoundTrip(t *testing.T) {
	roundTripTools(t)
	ctx := context.Background()
	v := secrets.Values{}.With("API_KEY", testSecret).With("DB_PASS", "db-password-value")
	ct, err := secrets.Encrypt(ctx, v, []string{recoveryPub, machinePub})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ct), testSecret) || strings.Contains(string(ct), "db-password-value") {
		t.Fatalf("ciphertext holds a value:\n%s", ct)
	}
	names, recipients, err := secrets.Ciphertext(ct)
	if err != nil || !slices.Equal(names, []string{"API_KEY", "DB_PASS"}) || !slices.Equal(recipients, []string{machinePub, recoveryPub}) {
		t.Fatalf("Ciphertext = %q, %q, %v", names, recipients, err)
	}
	p := paths(t)
	write(t, p.Key, "# public key: "+machinePub+"\nAGE-SECRET-KEY-1X\n")
	write(t, p.File, string(ct))
	r := secrets.Check(ctx, p, nil)
	if !r.KeyExists || r.Recipient != machinePub || !r.FileExists || len(r.FileSHA256) != 64 || !r.Decrypts || !slices.Equal(r.Names, names) || r.Problem != "" {
		t.Fatalf("Check = %+v", r)
	}
	write(t, p.Key, "# public key: age1other\nAGE-SECRET-KEY-1Y\n")
	if r := secrets.Check(ctx, p, nil); r.Decrypts || !strings.Contains(r.Problem, "Failed to get the data key") || len(r.Names) != 0 {
		t.Fatalf("Check with another key = %+v, want no decryption", r)
	}
	if r := secrets.Check(ctx, paths(t), nil); r.KeyExists || r.FileExists || r.Decrypts || !strings.Contains(r.Problem, "secrets init") {
		t.Fatalf("Check of nothing = %+v", r)
	}
}

// TestCiphertextRefusesPlaintext: a value not encrypted by sops, a nested value or a file with no sops metadata is
// refused, naming no value.
func TestCiphertextRefusesPlaintext(t *testing.T) {
	meta := "sops:\n    age:\n        - recipient: " + machinePub + "\n"
	for _, data := range []string{
		"API_KEY: " + testSecret + "\n" + meta,
		"API_KEY:\n    nested: ENC[x]\n" + meta,
		"API_KEY: ENC[AES256_GCM,data:eA==,type:str]\n",
		"1BAD: ENC[x]\n" + meta,
	} {
		if _, _, err := secrets.Ciphertext([]byte(data)); err == nil || strings.Contains(err.Error(), testSecret) {
			t.Errorf("Ciphertext(%q) = %v, want an error naming no value", data, err)
		}
	}
}

func TestCheckValue(t *testing.T) {
	for _, bad := range []string{"", "a\x00b"} {
		if secrets.CheckValue(bad) == nil {
			t.Errorf("CheckValue(%q) accepted", bad)
		}
	}
	if err := secrets.CheckValue(testSecret); err != nil {
		t.Fatal(err)
	}
	if got := (secrets.Paths{Key: "/h/.config/guildhall/age.key"}).RecoveryKey(); got != filepath.FromSlash("/h/.config/guildhall/recovery.key") {
		t.Fatalf("RecoveryKey = %q", got)
	}
}
