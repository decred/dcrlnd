//go:build dev
// +build dev

package dcrd

import (
	"testing"

	chainntnfstest "github.com/decred/dcrlnd/chainntnfs/test"
)

// TestInterfaces executes the generic notifier test suite against a dcrd
// powered chain notifier.
func TestInterfaces(t *testing.T) {
	chainntnfstest.TestInterfaces(t, "dcrd")
}
