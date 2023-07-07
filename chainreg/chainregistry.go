package chainreg

import (
	"time"

	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lncfg"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
	"google.golang.org/grpc"
)

// Config houses necessary fields that a chainControl instance needs to
// function.
type Config struct {
	// Decred defines the settings for the Decred chain.
	Decred *lncfg.Chain

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

	// WalletLoader is a pointer to an embedded wallet loader.
	WalletLoader *walletloader.Loader

	// Wallet is a pointer to the backing wallet instance.
	Wallet *wallet.Wallet

	// WalletConn is the grpc connection to a remote wallet when the chain
	// is running based on a remote wallet.
	WalletConn *grpc.ClientConn

	// WalletAccountNb is the root account from which the dcrlnd keys are
	// derived when running based on a remote wallet.
	WalletAccountNb int32

	// ActiveNetParams details the current chain we are on.
	ActiveNetParams DecredNetParams

	// FeeURL defines the URL for fee estimation we will use. This field is
	// optional.
	FeeURL string
}
