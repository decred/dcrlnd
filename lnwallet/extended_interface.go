package lnwallet

import (
	"errors"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/wire"
)

type WalletTransaction struct {
	RawTx         []byte
	Confirmations int32
	BlockHash     *chainhash.Hash
}

var ErrWalletTxNotExist = errors.New("tx does not exist in wallet")

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

	// GetWalletTransaction returns information about a transaction that
	// belongs to the wallet. If the transaction does not exist in the
	// wallet, then ErrWalletTxNotExist should be returned.
	GetWalletTransaction(tx chainhash.Hash) (*WalletTransaction, error)
}

// LockedOutput is a type that contains an outpoint of an UTXO and its lock lease
// information.
type LockedOutput struct {
	LockID     LockID
	Outpoint   wire.OutPoint
	Expiration time.Time
}
