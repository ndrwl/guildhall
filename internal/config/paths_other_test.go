//go:build !windows

package config

// hostPaths are repository paths whose absoluteness differs between platforms.
// Parse checks a repository path by the rules of the host it runs on.
var hostPaths = []struct {
	path     string
	absolute bool
}{
	{`/srv/src/tool`, true},
	{`C:\src\tool`, false},
	{`c:/src/tool`, false},
	{`\\server\share\tool`, false},
}
