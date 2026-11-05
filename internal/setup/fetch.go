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
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// maxFetch bounds a download.
const maxFetch = 1 << 30

// rename and mkdirTemp are the filesystem calls a test makes fail.
var (
	rename    = atomicfile.Rename
	mkdirTemp = os.MkdirTemp
)

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
	return rename(tmp.Name(), dest)
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

// unpackInto extracts the gzipped tar archive into a scratch directory beside dest, dropping strip leading elements
// of each name, and only then puts it at dest. Only directories and regular files are allowed, and no name may leave
// dest. A failed extraction leaves dest untouched.
func unpackInto(archive, dest string, strip int) error {
	work, err := mkdirTemp(filepath.Dir(dest), ".setup-unpack-*")
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
	return swapInto(work, dest)
}

// swapInto puts the fully extracted directory work at dest. An earlier install at dest (claim has checked the
// manifest owns it) is moved aside first, put back if work cannot be moved into place, and removed only once it has
// been replaced.
func swapInto(work, dest string) error {
	if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
		return rename(work, dest)
	} else if err != nil {
		return err
	}
	old, err := oldCopyName(dest)
	if err != nil {
		return err
	}
	if err := rename(dest, old); err != nil {
		return fmt.Errorf("unpack: move the earlier install %s aside: %w", dest, err)
	}
	if err := rename(work, dest); err != nil {
		if berr := rename(old, dest); berr != nil {
			return fmt.Errorf("unpack: move the new install into %s: %w; the earlier install is at %s and could not be put back: %v", dest, err, old, berr)
		}
		return fmt.Errorf("unpack: move the new install into %s: %w; the earlier install was put back from %s", dest, err, old)
	}
	// The new install is in place; an old copy that cannot be removed is a harmless leftover.
	_ = os.RemoveAll(old)
	return nil
}

// oldCopyName is the first dest.old-<n>, from 1, that does not exist, so a leftover from an earlier crash is kept.
func oldCopyName(dest string) (string, error) {
	for n := 1; ; n++ {
		p := fmt.Sprintf("%s.old-%d", dest, n)
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p, nil
		} else if err != nil {
			return "", err
		}
	}
}
