package config

// hostPaths are repository paths whose absoluteness differs between platforms.
// Parse checks a repository path by the rules of the host it runs on.
var hostPaths = []struct {
	path     string
	absolute bool
}{
	{`C:\src\tool`, true},
	{`c:/src/tool`, true},
	{`\\server\share\tool`, true},
	{`C:src\tool`, false},
	{`\src\tool`, false},
	{`/srv/src/tool`, false},
}
