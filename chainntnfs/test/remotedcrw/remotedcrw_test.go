//go:build dev
// +build dev

package remotedcrw

import (
	"testing"

	chainntnfstest "github.com/decred/dcrlnd/chainntnfs/test"
)

// TestInterfaces executes the generic notifier test suite against a remotedcrw
// powered chain notifier.
func TestInterfaces(t *testing.T) {
	chainntnfstest.TestInterfaces(t, "remotedcrw")
}
