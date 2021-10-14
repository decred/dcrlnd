package chainreg

import (
	"time"

	"github.com/decred/dcrlnd/btcwalletcompat"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
)

// WalletConfig encapsulates the config parameters needed to init a wallet
// backend.
type WalletConfig struct {
	// PrivatePass is the private wallet password to the underlying
	// btcwallet instance.
	PrivatePass []byte

	// PublicPass is the public wallet password to the underlying btcwallet
	// instance.
	PublicPass []byte

	// Birthday specifies the time the wallet was initially created.
	Birthday time.Time

	// RecoveryWindow specifies the address look-ahead for which to scan when
	// restoring a wallet.
	RecoveryWindow uint32

	// AccountNb is the root account from which the dcrlnd keys are
	// derived when running based on a remote wallet.
	AccountNb int32

	Syncer dcrwallet.WalletSyncer

	// CoinSelectionStrategy is the coin selection strategy to use when
	// sending coins on-chain.
	//
	// Note: not currently supported in dcrwallet.
	CoinSelectionStrategy btcwalletcompat.CoinSelectionStrategy
}
