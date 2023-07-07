package chainreg

import (
	"time"

	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lncfg"
)

// Config houses necessary fields that a chainControl instance needs to
// function.
type Config struct {
	// PrimaryChain is a function that returns our primary chain via its
	// ChainCode.
	PrimaryChain func() ChainCode

	// HeightHintCacheQueryDisable is a boolean that disables height hint
	// queries if true.
	HeightHintCacheQueryDisable bool

	// DcrdMode defines settings for connecting to a dcrd instance.
	DcrdMode *lncfg.DcrdConfig

	// DcrwMode defines settings for connecting to a dcrwallet instance.
	DcrwMode *lncfg.DcrwalletConfig

	// LocalChanDB is a pointer to the local backing channel database.
	LocalChanDB *channeldb.DB

	// RemoteChanDB is a pointer to the remote backing channel database.
	RemoteChanDB *channeldb.DB

	// PrivateWalletPw is the private wallet password to the underlying
	// btcwallet instance.
	PrivateWalletPw []byte

	// PublicWalletPw is the public wallet password to the underlying btcwallet
	// instance.
	PublicWalletPw []byte

	// Birthday specifies the time the wallet was initially created.
	Birthday time.Time

	// RecoveryWindow specifies the address look-ahead for which to scan when
	// restoring a wallet.
	RecoveryWindow uint32

	// Wallet is a pointer to the backing wallet instance.
	Wallet *wallet.Wallet

	// ActiveNetParams details the current chain we are on.
	ActiveNetParams DecredNetParams

	// FeeURL defines the URL for fee estimation we will use. This field is
	// optional.
	FeeURL string
}
