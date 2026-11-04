package setup

// The fetch act is the Go form of Firstmate's bin/fm-install-herdr.sh download at ndrwl/firstmate
// 2add138ffe92309787dbbedf375beb4f05704f7b (MIT, see NOTICE): a user-directory install,
// with no administrator rights, of a file whose sha256 is checked before anything is put in place. Unlike it, the
// version, URL and checksum come from the caller (a plan's pins), and a wrong checksum is refused, not warned about.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxFetch bounds a download.
const maxFetch = 1 << 30

func (e *env) fetch(ctx context.Context, f Fetch) error {
	rawURL, want, dest := e.expand(f.URL), strings.ToLower(e.expand(f.SHA256)), filepath.Clean(e.expand(f.Dest))
	if rawURL == "" || want == "" {
		return fmt.Errorf("fetch needs a url and a sha256; give them with --set (the step file's params: %s)", paramNames(e.s))
	}
	if !digestPattern.MatchString(want) {
		return fmt.Errorf("fetch sha256 %q is not 64 hex digits", want)
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "https" && (u.Scheme != "http" || !loopback(u.Hostname()))) {
		return fmt.Errorf("fetch url %q is not https", rawURL)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".setup-fetch-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := e.f.HTTP.Do(req)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		return fmt.Errorf("fetch %s: %s", rawURL, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, maxFetch+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if n > maxFetch {
		return fmt.Errorf("fetch %s: larger than %d bytes", rawURL, maxFetch)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%w: %s has sha256 %s, the plan names %s; nothing was installed", ErrChecksum, rawURL, got, want)
	}
	if f.Unpack == "tar.gz" {
		return unpackInto(tmp.Name(), dest, f.Strip)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dest)
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func paramNames(s Steps) string {
	var names []string
	for k := range s.Params {
		names = append(names, k)
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(sortedStrings(names), ", ")
}

// unpackInto extracts the gzipped tar archive into a new directory dest, by way of a temporary directory beside it,
// dropping strip leading elements of each name. Only directories and regular files are allowed, and no name may
// leave dest.
func unpackInto(archive, dest string, strip int) error {
	if _, err := os.Lstat(dest); err == nil {
		// claim has checked the manifest owns dest; an owned earlier install is replaced whole.
		if err := os.RemoveAll(dest); err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp(filepath.Dir(dest), ".setup-unpack-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("unpack: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("unpack: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(hdr.Name, `\`) {
			return fmt.Errorf("unpack: entry %q leaves the destination", hdr.Name)
		}
		parts := strings.Split(name, "/")
		if len(parts) <= strip {
			continue
		}
		name = path.Join(parts[strip:]...)
		target := filepath.Join(work, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(hdr.Mode)&0o755|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(tr, maxFetch))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unpack: entry %q is neither a directory nor a regular file", hdr.Name)
		}
	}
	return os.Rename(work, dest)
}
