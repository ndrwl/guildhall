package artisan

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ndrwl/guildhall/internal/atomicfile"
)

// ErrInvalidProcessRecord is returned for a process record without a positive
// PID, a token or a role, or one not stored under its own file name.
var ErrInvalidProcessRecord = errors.New("artisan: invalid process record")

// ProcessRecord is one process in an inventory directory: an identity with a
// role. The fake Artisan, the test worker and the lab harness write and read
// inventories only through RecordProcess and ReadProcessRecords
// (docs/artisan.md#harness).
type ProcessRecord struct {
	Identity
	Role string `json:"role"`
}

// file is the record's file name, <role>-<pid>.json, or an error wrapping
// ErrInvalidProcessRecord when r is not a valid record.
func (r ProcessRecord) file() (string, error) {
	switch {
	case r.PID <= 0:
		return "", fmt.Errorf("%w: PID %d is not positive", ErrInvalidProcessRecord, r.PID)
	case r.Token == "":
		return "", fmt.Errorf("%w: PID %d has no token", ErrInvalidProcessRecord, r.PID)
	case r.Role == "" || r.Role == "." || r.Role == ".." || strings.ContainsAny(r.Role, `/\`):
		return "", fmt.Errorf("%w: role %q is not a file-name component", ErrInvalidProcessRecord, r.Role)
	}
	return fmt.Sprintf("%s-%d.json", r.Role, r.PID), nil
}

// RecordProcess writes rec into the inventory directory dir as
// <role>-<pid>.json, atomically, creating dir if needed. A record that is not
// valid is refused with ErrInvalidProcessRecord and nothing is written.
func RecordProcess(dir string, rec ProcessRecord) error {
	name, err := rec.file()
	if err != nil {
		return err
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, name), b)
}

// ReadProcessRecords reads every record in the inventory directory dir, in
// file-name order. Files not named *.json are ignored. A record that is not
// valid is refused with ErrInvalidProcessRecord naming its file; a missing
// directory is an error matching fs.ErrNotExist.
func ReadProcessRecords(dir string) ([]ProcessRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ProcessRecord
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		n := filepath.Join(dir, e.Name())
		b, err := atomicfile.Read(n)
		if err != nil {
			return nil, err
		}
		var r ProcessRecord
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalidProcessRecord, n, err)
		}
		name, err := r.file()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if name != e.Name() {
			return nil, fmt.Errorf("%w: %s holds %s", ErrInvalidProcessRecord, n, name)
		}
		out = append(out, r)
	}
	return out, nil
}
