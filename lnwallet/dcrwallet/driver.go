package dcrwallet

import (
	"fmt"

	"github.com/decred/dcrlnd/lnwallet"
)

const (
	walletType = "dcrwallet"
)

var (
	availableBackends = []string{"dcrd", "spv"}
)

// createNewWallet creates a new instance of DcrWallet given the proper list of
// initialization parameters. This function is the factory function required to
// properly create an instance of the lnwallet.WalletDriver struct for
// DcrWallet.
func createNewWallet(args ...interface{}) (lnwallet.WalletController, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("incorrect number of arguments to .New(...), "+
			"expected 2, instead passed %v", len(args))
	}

	config, ok := args[0].(*Config)
	if !ok {
		return nil, fmt.Errorf("first argument to dcrdnotifier.New is " +
			"incorrect, expected a *rpcclient.ConnConfig")
	}

	return New(*config)
}

// init registers a driver for the DcrWallet concrete implementation of the
// lnwallet.WalletController interface.
func init() {
	// Register the driver.
	driver := &lnwallet.WalletDriver{
		WalletType: walletType,
		New:        createNewWallet,
		BackEnds:   func() []string { return availableBackends },
	}

	if err := lnwallet.RegisterWallet(driver); err != nil {
		panic(fmt.Sprintf("failed to register wallet driver '%s': %v",
			walletType, err))
	}
}
