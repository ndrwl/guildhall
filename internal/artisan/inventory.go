package artisan

import "errors"

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

// Process returns r; it makes ProcessRecord an InventoryRecord.
func (r ProcessRecord) Process() ProcessRecord { return r }

// InventoryRecord is a record RecordProcess writes: a ProcessRecord, or a
// wider type embedding one whose other fields are written beside the identity
// and role, read back with ReadProcessRecordsAs.
type InventoryRecord interface {
	Process() ProcessRecord
}

// RecordProcess writes rec into the inventory directory dir as
// <role>-<pid>.json, atomically, creating dir if needed. A record that is not
// valid is refused with ErrInvalidProcessRecord and nothing is written.
func RecordProcess(dir string, rec InventoryRecord) error {
	return errors.New("not implemented")
}

// ReadProcessRecords reads every record in the inventory directory dir, in
// file-name order. Files not named *.json are ignored. A record that is not
// valid is refused with ErrInvalidProcessRecord naming its file; a missing
// directory is an error matching fs.ErrNotExist.
func ReadProcessRecords(dir string) ([]ProcessRecord, error) {
	return ReadProcessRecordsAs[ProcessRecord](dir)
}

// ReadProcessRecordsAs is ReadProcessRecords decoding each file into the
// wider type R, which RecordProcess wrote, and validating R's ProcessRecord.
func ReadProcessRecordsAs[R InventoryRecord](dir string) ([]R, error) {
	return nil, errors.New("not implemented")
}
