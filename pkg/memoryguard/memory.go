package memoryguard

import "errors"

var (
	ErrUnsupported  = errors.New("available memory monitoring is unsupported on this platform")
	ErrLimitReached = errors.New("available memory safety threshold reached")
)
