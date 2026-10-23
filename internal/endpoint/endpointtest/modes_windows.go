package endpointtest

import "testing"

// checkModes checks nothing: on Windows the socket inherits its directory's ACL.
func checkModes(*testing.T, string) {}
