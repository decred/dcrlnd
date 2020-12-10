package embedded_dcrd

import (
	"testing"

	lnwallettest "github.com/decred/dcrlnd/lnwallet/test"
)

// TestLightningWallet tests LightningWallet with an embedded dcrwallet powered
// by the spv network against our suite of interface tests.
func TestLightningWallet(t *testing.T) {
	lnwallettest.TestLightningWallet(t, "dcrwallet", "spv")

}
