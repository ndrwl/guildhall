//go:build !windows

package adapter

const nativeStopSupported = false

func nativeCreationTime() (uint64, error) { return 0, ErrStopUnsupported }

func openNativeWorker(uint32, uint64, string) (nativeWorker, error) {
	return nil, ErrStopUnsupported
}
