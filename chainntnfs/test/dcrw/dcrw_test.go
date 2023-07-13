//go:build dev
// +build dev

package dcrw

import (
	"testing"

	chainntnfstest "github.com/decred/dcrlnd/chainntnfs/test"
)

// TestDcrwChainNtfns executes the generic notifier test suite against a dcrw
// powered chain notifier.
func TestDcrwChainNtfns(t *testing.T) {
	t.Run("dcrd", func(t *testing.T) {
		chainntnfstest.TestInterfaces(t, "dcrw", "dcrd")
	})
	t.Run("spv", func(t *testing.T) {
		chainntnfstest.TestInterfaces(t, "dcrw", "spv")
	})
}
