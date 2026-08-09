//go:build !linux && !darwin && !windows

package memoryguard

func Available() (uint64, error) {
	return 0, ErrUnsupported
}
