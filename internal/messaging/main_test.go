package messaging_test

import (
	"os"
	"testing"

	"github.com/ndrwl/guildhall/internal/store"
)

func TestMain(m *testing.M) {
	store.SkipSyncForTests()
	os.Exit(m.Run())
}
