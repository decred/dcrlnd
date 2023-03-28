package lnwallet

import (
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
)

// ExtendedWalletController offers extended actions for the wallet (ones defined
// only in dcrlnd).
type ExtendedWalletController interface {
	// DeriveNextAccount derives a new account master key and stores it
	// as a new account with the specified name.
	DeriveNextAccount(name string) error

	// ExportPrivKey returns the private key for a wallet-controlled
	// address.
	ExportPrivKey(addr stdaddr.Address) (*secp256k1.PrivateKey, error)

	// RescanWallet performs a wallet rescan for transactions.
	RescanWallet(startHeight int32, progress func(height int32) error) error
}
