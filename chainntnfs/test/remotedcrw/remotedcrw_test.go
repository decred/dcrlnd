//go:build dev
// +build dev

package remotedcrw

import (
	"testing"

	chainntnfstest "github.com/decred/dcrlnd/chainntnfs/test"
)

// TestRemoteDcrwChainNtfns executes the generic notifier test suite against a remotedcrw
// powered chain notifier.
func TestRemoteDcrwChainNtfns(t *testing.T) {
	t.Run("dcrd", func(t *testing.T) {
		chainntnfstest.TestInterfaces(t, "remotedcrw", "dcrd")
	})
	t.Run("spv", func(t *testing.T) {
		chainntnfstest.TestInterfaces(t, "remotedcrw", "spv")
	})
}
