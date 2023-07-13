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
	chainntnfstest.TestInterfaces(t, "dcrw")
}
