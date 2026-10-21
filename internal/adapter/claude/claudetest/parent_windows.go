package claudetest

import "os"

// parentGone is closed once the stub's parent has exited, observed through a
// handle on it; a parent that cannot be opened has already gone.
func parentGone() <-chan struct{} {
	gone := make(chan struct{})
	p, err := os.FindProcess(os.Getppid())
	if err != nil {
		close(gone)
		return gone
	}
	go func() {
		_, _ = p.Wait()
		close(gone)
	}()
	return gone
}
