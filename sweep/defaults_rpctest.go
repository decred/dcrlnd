//go:build rpctest
// +build rpctest

package sweep

import (
	"time"
)

var (
	// DefaultBatchWindowDuration specifies duration of the sweep batch
	// window. The sweep is held back during the batch window to allow more
	// inputs to be added and thereby lower the fee per input.
	//
	// To speed up integration tests waiting for a sweep to happen, the
	// batch window is shortened.
	//
	// No integration tests currently rely on sweeping multiple inputs
	// simultaneously so this can be a really short time.
	DefaultBatchWindowDuration = 250 * time.Millisecond
)
