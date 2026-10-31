package workbench

import (
	"bytes"
	"io"
	"os"
)

// lineTail splits appended bytes into complete lines, keeping a partial last line until its newline arrives. A
// line longer than limit is dropped, or, with split, passed on in limit-sized pieces, so memory stays bounded.
type lineTail struct {
	limit    int
	split    bool
	partial  []byte
	dropping bool
}

func (t *lineTail) feed(p []byte) [][]byte {
	var lines [][]byte
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i
		}
		chunk := p[:end]
		for !t.dropping && len(t.partial)+len(chunk) > t.limit {
			if !t.split {
				t.partial, t.dropping = nil, true
				break
			}
			take := t.limit - len(t.partial)
			lines = append(lines, append(bytes.Clone(t.partial), chunk[:take]...))
			t.partial, chunk = t.partial[:0], chunk[take:]
		}
		if !t.dropping {
			t.partial = append(t.partial, chunk...)
		}
		if i < 0 {
			break
		}
		if !t.dropping {
			lines = append(lines, bytes.Clone(t.partial))
		}
		t.partial, t.dropping = t.partial[:0], false
		p = p[i+1:]
	}
	return lines
}

// rest returns the partial last line, if any, and forgets it.
func (t *lineTail) rest() ([]byte, bool) {
	r, ok := bytes.Clone(t.partial), len(t.partial) > 0 && !t.dropping
	t.partial, t.dropping = nil, false
	return r, ok
}

// tailChunk bounds one read of a tailed file.
const tailChunk = 1 << 20

// fileTail reads what has been appended to a file since the last read. A missing file reads as nothing.
type fileTail struct {
	path string
	f    *os.File
	buf  []byte
}

// read returns at most tailChunk bytes appended since the last read, and whether the file exists. The bytes are
// valid until the next read.
func (t *fileTail) read() ([]byte, bool) {
	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return nil, false
		}
		t.f, t.buf = f, make([]byte, tailChunk)
	}
	n, _ := io.ReadFull(t.f, t.buf)
	return t.buf[:n], true
}

func (t *fileTail) close() {
	if t.f != nil {
		_ = t.f.Close()
	}
}
