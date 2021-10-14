//go:build dev
// +build dev

package chainntnfs

import "github.com/decred/dcrd/chaincfg/chainhash"

// TestChainNotifier enables the use of methods that are only present during
// testing for ChainNotifiers.
type TestChainNotifier interface {
	ChainNotifier

	// UnsafeStart enables notifiers to start up with a specific best block.
	// Used for testing.
	UnsafeStart(int64, *chainhash.Hash, int64, func() error) error
}
