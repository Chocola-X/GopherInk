//go:build darwin && !cgo

package memoryguard

func Available() (uint64, error) {
	return 0, ErrUnsupported
}
