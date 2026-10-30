package cilog_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ndrwl/guildhall/internal/cilog"
)

var update = flag.Bool("update", false, "rewrite the golden excerpts")

// generated are the fixture logs too large to keep by hand, built the same way at every run.
func generated() map[string]string {
	var long, wide strings.Builder
	long.WriteString("2026-10-29T10:00:00.0000000Z ##[group]Run go test ./...\n")
	for i := range 300 {
		fmt.Fprintf(&long, "2026-10-29T10:00:%02d.0000000Z === RUN   TestCase%03d\n", i%60, i)
	}
	long.WriteString("2026-10-29T10:01:00.0000000Z ##[error]Process completed with exit code 1.\n")
	wide.WriteString("2026-10-29T10:00:00.0000000Z ##[group]Run go vet ./...\n")
	for i := range 150 {
		fmt.Fprintf(&wide, "2026-10-29T10:00:00.0000000Z line %03d %s\n", i, strings.Repeat("x", 400))
	}
	wide.WriteString("2026-10-29T10:01:00.0000000Z ##[error]Process completed with exit code 1.\n")
	// The shape of a real job log as gh api prints it (docs/evidence/failed-job-logs-redirect-2026-10-30.txt): a
	// byte order mark before the first timestamp, CRLF line ends, and ANSI colour codes from the step's commands.
	crlf := strings.Join([]string{
		"\xef\xbb\xbf2026-10-30T09:00:00.1234567Z ##[group]Run actions/checkout@v4",
		"2026-10-30T09:00:00.2000000Z with:",
		"2026-10-30T09:00:01.0000000Z ##[endgroup]",
		"2026-10-30T09:00:02.0000000Z ##[group]Run go test ./...",
		"2026-10-30T09:00:02.1000000Z \x1b[36;1mgo test ./...\x1b[0m",
		"2026-10-30T09:00:02.2000000Z shell: C:\\Program Files\\PowerShell\\7\\pwsh.EXE -command \". '{0}'\"",
		"2026-10-30T09:00:02.3000000Z ##[endgroup]",
		"2026-10-30T09:00:09.0000000Z \x1b[31m--- FAIL: TestCheckLog (0.01s)\x1b[0m",
		"2026-10-30T09:00:09.1000000Z FAIL\tgithub.com/acme/widgets\t0.412s",
		"2026-10-30T09:00:09.2000000Z ##[error]Process completed with exit code 1.",
		"2026-10-30T09:00:10.0000000Z ##[group]Run actions/upload-artifact@v4",
		"2026-10-30T09:00:10.1000000Z ##[endgroup]",
		"2026-10-30T09:00:11.0000000Z Cleaning up orphan processes",
		"",
	}, "\r\n")
	return map[string]string{"more-than-200-lines": long.String(), "section-over-32kib": wide.String(), "bom-crlf-escapes": crlf}
}

// TestExcerptGolden pins the excerpt of each fixture log: an error in a middle step, no error, a step of more
// than 200 lines, a step over 32 KiB, and a log with a byte order mark, CRLF line ends and escape sequences.
func TestExcerptGolden(t *testing.T) {
	logs := generated()
	files, err := filepath.Glob(filepath.Join("testdata", "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		logs[strings.TrimSuffix(filepath.Base(f), ".log")] = string(data)
	}
	if len(logs) != 5 {
		t.Fatalf("%d fixture logs, want 5", len(logs))
	}
	for name, log := range logs {
		t.Run(name, func(t *testing.T) {
			got := cilog.Excerpt([]byte(log))
			golden := filepath.Join("testdata", name+".excerpt")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("Excerpt = %q\nwant %q", got, want)
			}
			if lines := strings.Count(got, "\n") + 1; lines > cilog.ExcerptLines || len(got) > cilog.ExcerptBytes {
				t.Errorf("excerpt has %d lines and %d bytes, past its bounds", lines, len(got))
			}
			if strings.Contains(got, "2026-10-29T") || strings.Contains(got, "2026-10-30T") {
				t.Error("excerpt keeps a timestamp")
			}
			if strings.ContainsAny(got, "\r\x1b") || strings.Contains(got, "\xef\xbb\xbf") {
				t.Errorf("excerpt keeps a CR, an escape sequence or a byte order mark: %q", got)
			}
		})
	}
}

// TestExcerptEscapes: colour codes, other control sequences, an operating system command and a two-byte escape
// are removed, and a ##[group]Run or ##[error] marker wrapped in them is still matched.
func TestExcerptEscapes(t *testing.T) {
	log := "##[group]Run one\nskipped\n\x1b[1m##[group]Run two\x1b[0m\n\x1b]0;title\x07\x1b[2K\x1b[?25lred \x1b[31;1mtext\x1b[m\x1bM\n\x1b[31m##[error]boom\n##[group]Run three\n"
	if got, want := cilog.Excerpt([]byte(log)), "##[group]Run two\nred text\n##[error]boom"; got != want {
		t.Errorf("Excerpt = %q, want %q", got, want)
	}
}

func TestExcerptBounds(t *testing.T) {
	if got := cilog.Excerpt(nil); got != "" {
		t.Errorf("Excerpt(nil) = %q", got)
	}
	one := strings.Repeat("é", cilog.ExcerptBytes)
	if got := cilog.Excerpt([]byte(one + "\n")); len(got) > cilog.ExcerptBytes || !strings.HasSuffix(one, got) || !strings.HasPrefix(got, "é") {
		t.Errorf("Excerpt of one long line is %d bytes, want its tail within %d from a rune boundary", len(got), cilog.ExcerptBytes)
	}
}
