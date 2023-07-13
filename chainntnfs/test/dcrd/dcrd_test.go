//go:build dev
// +build dev

package dcrd

import (
	"testing"

	chainntnfstest "github.com/decred/dcrlnd/chainntnfs/test"
)

// TestDcrdChainNtfns executes the generic notifier test suite against a dcrd
// powered chain notifier.
func TestDcrdChainNtfns(t *testing.T) {
	chainntnfstest.TestInterfaces(t, "dcrd")
}
