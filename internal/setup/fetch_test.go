package setup_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/setup"
)

func serve(t *testing.T, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	t.Cleanup(srv.Close)
	return srv.URL + "/download"
}

// fetchDemo is a cross-OS fetch step checked with the setuptool stub.
const fetchDemo = `schema = "guildhall.steps/1"
name = "fetch-demo"

[params]
tool = ""
url = ""
sum = ""

[[step]]
id = "payload"
title = "Fetch the payload"
tag = "fixable"
os = ["linux", "darwin", "windows"]
check = ["${tool}", "cat", "${home}/bin/payload"]
expect = "payload-v2"
undo = "Remove ~/bin/payload."

[step.fetch]
url = "${url}"
sha256 = "${sum}"
dest = "${home}/bin/payload"
`

func TestFetchRefusesWrongChecksum(t *testing.T) {
	sb := newSandbox(t)
	body := []byte("payload-v2\n")
	url := serve(t, body)
	s := parse(t, fetchDemo)
	f := sb.facts
	f.Vars = map[string]string{"tool": tool(), "url": url, "sum": setup.Digest([]byte("something else"))}
	_, err := setup.Fix(context.Background(), s, f, []string{"payload"}, nil)
	if !errors.Is(err, setup.ErrChecksum) {
		t.Fatalf("wrong checksum = %v, want ErrChecksum", err)
	}
	if _, err := os.Stat(filepath.Join(sb.home, "bin", "payload")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused download was installed: %v", err)
	}
	f.Vars["sum"] = setup.Digest(body)
	r, err := setup.Fix(context.Background(), s, f, []string{"payload"}, nil)
	if err != nil || len(r.Gaps) != 0 {
		t.Fatalf("right checksum = %+v, %v", r.Gaps, err)
	}
	f.Vars["url"] = strings.Replace(url, "http://", "ftp://", 1)
	if err := os.Remove(filepath.Join(sb.home, "bin", "payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Fix(context.Background(), s, f, []string{"payload"}, nil); err == nil || !strings.Contains(err.Error(), "not https") {
		t.Errorf("non-https url = %v", err)
	}
}

func requireUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the built-in herdr and go step files are for Linux and macOS; slice 5 adds their Windows forms")
	}
}

func TestBuiltinHerdr(t *testing.T) {
	requireUnix(t)
	sb := newSandbox(t)
	t.Setenv("PATH", "/usr/bin:/bin") // no herdr on PATH; sh and its tools only
	body := []byte("#!/bin/sh\necho herdr 0.5.1\n")
	s, err := setup.LoadBuiltin("herdr", "")
	if err != nil {
		t.Fatal(err)
	}
	f := setup.Facts{Home: sb.home, SetupDir: sb.setupDir, Vars: map[string]string{
		"herdr_version": "0.5.1", "herdr_url": serve(t, body), "herdr_sha256": setup.Digest([]byte("tampered")),
	}}
	r, err := setup.Doctor(context.Background(), s, f)
	if err != nil || len(r.Gaps) != 1 || r.Gaps[0].ID != "herdr" || r.Gaps[0].Tag != setup.Fixable {
		t.Fatalf("doctor = %+v, %v", r, err)
	}
	if _, err := setup.Fix(context.Background(), s, f, []string{"herdr"}, nil); !errors.Is(err, setup.ErrChecksum) {
		t.Fatalf("wrong checksum = %v, want ErrChecksum", err)
	}
	f.Vars["herdr_sha256"] = setup.Digest(body)
	r, err = setup.Fix(context.Background(), s, f, []string{"herdr"}, nil)
	if err != nil || len(r.Gaps) != 0 {
		t.Fatalf("fix = %+v, %v", r.Gaps, err)
	}
	bin := filepath.Join(sb.home, ".local", "bin", "herdr")
	if m, _ := setup.ReadManifest(sb.setupDir); !m.Owns(bin) {
		t.Errorf("manifest does not own %s", bin)
	}
	// Another version is a gap again, and the doctor's check reads the installed one.
	f.Vars["herdr_version"] = "0.6.0"
	if r, _ := setup.Doctor(context.Background(), s, f); len(r.Gaps) != 1 {
		t.Errorf("version 0.6.0 against 0.5.1 = %+v", r.Gaps)
	}
}

func TestBuiltinGoUnpacks(t *testing.T) {
	requireUnix(t)
	sb := newSandbox(t)
	// An older Go already on PATH (as on CI runners) neither hides the install in ~/.local/go nor satisfies
	// go-on-path.
	old := t.TempDir()
	if err := os.WriteFile(filepath.Join(old, "go"), []byte("#!/bin/sh\necho go version go1.26.10 linux/amd64\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", old+string(os.PathListSeparator)+"/usr/bin:/bin")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	script := []byte("#!/bin/sh\necho go version go1.27.1 linux/amd64\n")
	for _, h := range []tar.Header{
		{Name: "go/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "go/bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "go/bin/go", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(script))},
	} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write(script)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	s, err := setup.LoadBuiltin("go", "")
	if err != nil {
		t.Fatal(err)
	}
	f := setup.Facts{Home: sb.home, SetupDir: sb.setupDir, Vars: map[string]string{
		"go_version": "1.27.1", "go_url": serve(t, buf.Bytes()), "go_sha256": setup.Digest(buf.Bytes()),
	}}
	r, err := setup.Fix(context.Background(), s, f, []string{"go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Gaps) != 1 || r.Gaps[0].ID != "go-on-path" || r.Gaps[0].Tag != setup.Human {
		t.Errorf("gaps after fixing go = %+v, want only go-on-path", r.Gaps)
	}
	if _, err := os.Stat(filepath.Join(sb.home, ".local", "go", "bin", "go")); err != nil {
		t.Errorf("go not unpacked: %v", err)
	}
}

func TestUnpackRefusesEscapes(t *testing.T) {
	sb := newSandbox(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "top/../../evil", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_ = gz.Close()
	src := strings.Replace(fetchDemo, `dest = "${home}/bin/payload"`, "dest = \"${home}/bin/payload\"\nunpack = \"tar.gz\"\nstrip = 1", 1)
	s := parse(t, src)
	f := sb.facts
	f.Vars = map[string]string{"tool": tool(), "url": serve(t, buf.Bytes()), "sum": setup.Digest(buf.Bytes())}
	if _, err := setup.Fix(context.Background(), s, f, nil, nil); err == nil || !strings.Contains(err.Error(), "leaves the destination") {
		t.Errorf("escaping entry = %v", err)
	}
	if _, err := os.Stat(filepath.Join(sb.home, "evil")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("escaping entry written: %v", err)
	}
}
