package dcrwallet

import (
	"path/filepath"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"

	"decred.org/dcrwallet/v4/wallet"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"

	// This is required to register bdb as a valid walletdb driver. In the
	// init function of the package, it registers itself. The import is used
	// to activate the side effects w/o actually binding the package name to
	// a file-level variable.
	_ "decred.org/dcrwallet/v4/wallet/drivers/bdb"
)

// WalletSyncer is an exported interface for the available wallet sync backends
// (RPC, SPV, etc). While this interface is exported to ease construction of a
// Config structure, only implementations provided by this package are
// supported, since currently the implementation is tightly coupled to the
// DcrWallet struct.
//
// The current backend implementations also implement the BlockChainIO
// interface.
type WalletSyncer interface {
	start(w *DcrWallet) error
	stop()
	waitForShutdown()
}

// Config is a struct which houses configuration parameters which modify the
// instance of DcrWallet generated by the New() function.
type Config struct {
	// DataDir is the name of the directory where the wallet's persistent
	// state should be stored.
	DataDir string

	// LogDir is the name of the directory which should be used to store
	// generated log files.
	LogDir string

	// PrivatePass is the private password to the underlying dcrwallet
	// instance. Without this, the wallet cannot be decrypted and operated.
	PrivatePass []byte

	// PublicPass is the optional public password to dcrwallet. This is
	// optionally used to encrypt public material such as public keys and
	// scripts.
	PublicPass []byte

	// HdSeed is an optional seed to feed into the wallet. If this is
	// unspecified, a new seed will be generated.
	HdSeed []byte

	// Birthday specifies the time at which this wallet was initially
	// created. It is used to bound rescans for used addresses.
	Birthday time.Time

	// FeeEstimator is an instance of the fee estimator interface which
	// will be used by the wallet to dynamically set transaction fees when
	// crafting transactions.
	FeeEstimator chainfee.Estimator

	// RecoveryWindow specifies the address look-ahead for which to scan
	// when restoring a wallet. The recovery window will apply to all
	// default BIP44 derivation paths.
	RecoveryWindow uint32

	// Syncer stores a specific implementation of a WalletSyncer (either an
	// RPC syncer or a SPV syncer) capabale of maintaining the wallet
	// backend synced to the chain.
	Syncer WalletSyncer

	// ChainIO is a direct connection to the blockchain IO driver needed by
	// the wallet.
	//
	// TODO(decred) Ideally this should be performed by wallet operations
	// but not all operations needed by the drivers are currently
	// implemented in the wallet.
	ChainIO lnwallet.BlockChainIO

	// NetParams is the net parameters for the target chain.
	NetParams *chaincfg.Params

	// Wallet is an unlocked wallet instance that is set if the
	// UnlockerService has already opened and unlocked the wallet. If this
	// is nil, then a wallet might have just been created or is simply not
	// encrypted at all, in which case it should be attempted to be loaded
	// normally when creating the DcrWallet.
	Wallet *wallet.Wallet

	// Loader is the loader used to initialize the Wallet field. If Wallet
	// is specified, then Loader MUST be specified as well.
	Loader *walletloader.Loader

	// BlockCache is an optional in-memory block cache.
	BlockCache *blockcache.BlockCache

	DB *channeldb.DB
}

// NetworkDir returns the directory name of a network directory to hold wallet
// files.
func NetworkDir(dataDir string, chainParams *chaincfg.Params) string {
	netname := chainParams.Name
	switch chainParams.Net {
	case 0x48e7a065: // testnet2
		netname = "testnet2"
	case wire.TestNet3:
		netname = "testnet3"
	}
	return filepath.Join(dataDir, netname)
}
