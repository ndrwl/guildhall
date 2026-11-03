package claude

import "testing"

// SetLookupEnv makes Session read the inherited environment through f for the rest of the test.
func SetLookupEnv(t *testing.T, f func(string) (string, bool)) {
	old := lookupEnv
	lookupEnv = f
	t.Cleanup(func() { lookupEnv = old })
}
