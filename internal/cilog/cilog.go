// Package cilog is the CI log cache: the failed job logs Guildhall fetches from the forge, kept on disk in
// <state>/<repo>/<task>/ci-logs/<job>.log so validate and guildhall ci log fetch each one once, and the log
// excerpt validate puts into a failed result (docs/interpreter.md#validate).
//
// The cache is disposable: it holds no store document, guildhall task files never lists it, and deleting it at
// any time only makes the next read fetch the log again.
package cilog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
	"github.com/ndrwl/guildhall/internal/forge"
)

// Reader reads one job's log from the forge; forge.GitHub and forgetest.Fake implement it.
type Reader interface {
	CheckLog(ctx context.Context, c forge.Commit, job string, limit int64) (forge.CheckLog, error)
}

// Log is one failed job log, from the cache or just fetched.
type Log struct {
	// Path is the log's file in the cache.
	Path string
	// Text is the log, without the cache's header line.
	Text []byte
	// Dropped is how many bytes were cut from the log's head to keep it within the limit it was fetched with.
	Dropped int64
}

// Dir is the CI log cache of task id of repository repo, under the state directory state.
func Dir(state, repo, id string) string {
	return filepath.Join(state, repo, id, "ci-logs")
}

// ValidJob reports whether job is a GitHub Actions job id: digits only, so it names a file in the cache.
func ValidJob(job string) bool {
	return job != "" && len(job) <= 20 && strings.Trim(job, "0123456789") == ""
}

// header is the first line of a cached log cut short, naming how many bytes were dropped from its head.
var header = regexp.MustCompile(`^\[guildhall: ([0-9]+) bytes dropped from the head of this log\]\n`)

// Get returns job's log from the cache in dir, or fetches it with r, at most limit bytes of its tail, and caches it.
// A cached log is written to a temporary file and renamed into place, so a concurrent reader sees the whole log or
// none, and a failed fetch leaves nothing behind. A job that is not an Actions job id, or a limit of zero or less,
// is forge.ErrLogUnavailable without reading the cache or the forge.
func Get(ctx context.Context, r Reader, dir string, c forge.Commit, job string, limit int64) (Log, error) {
	switch {
	case !ValidJob(job):
		return Log{}, &forge.LogUnavailableError{Reason: "not a GitHub Actions job"}
	case limit <= 0:
		return Log{}, &forge.LogUnavailableError{Reason: "log fetching is off"}
	}
	p := filepath.Join(dir, job+".log")
	data, err := atomicfile.Read(p)
	switch {
	case err == nil:
		return parse(p, data)
	case !errors.Is(err, fs.ErrNotExist):
		return Log{}, err
	case r == nil:
		return Log{}, &forge.LogUnavailableError{Reason: "forge does not provide logs"}
	}
	l, err := r.CheckLog(ctx, c, job, limit)
	if err != nil {
		return Log{}, err
	}
	var out bytes.Buffer
	if l.Dropped > 0 {
		fmt.Fprintf(&out, "[guildhall: %d bytes dropped from the head of this log]\n", l.Dropped)
	}
	out.Write(l.Text)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Log{}, err
	}
	if err := atomicfile.Write(p, out.Bytes()); err != nil {
		return Log{}, err
	}
	return Log{Path: p, Text: l.Text, Dropped: l.Dropped}, nil
}

func parse(p string, data []byte) (Log, error) {
	m := header.FindSubmatch(data)
	if m == nil {
		return Log{Path: p, Text: data}, nil
	}
	n, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return Log{}, fmt.Errorf("%s: %w", p, err)
	}
	return Log{Path: p, Text: data[len(m[0]):], Dropped: n}, nil
}
