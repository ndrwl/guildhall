package store

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	SkipSyncForTests()
	os.Exit(m.Run())
}
