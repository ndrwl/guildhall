package forge

import "context"

// ReadTestResultsCapped is GitHub.TestResults within the given artifact and stream caps in place of
// MaxTestArtifactBytes and MaxTestStreamBytes, so the external tests can reach the caps with small artifacts.
func ReadTestResultsCapped(ctx context.Context, g GitHub, c Commit, artifact, stream int64) (TestResults, error) {
	return g.testResults(ctx, c, testCaps{artifact: artifact, stream: stream})
}
