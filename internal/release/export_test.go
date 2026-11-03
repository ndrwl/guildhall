package release

// The internal test helpers and names the external package release_test's tests use.
var (
	NewHome       = newHome
	Good          = good
	Reports       = reports
	IncomingEmpty = incomingEmpty
	TestBinary    = binary
	InstalledAt   = installedAt
)

const (
	LockWait       = lockWait
	MetadataFile   = metadataFile
	MetadataSchema = metadataSchema
)

// Metadata is release.json.
type Metadata = metadata
