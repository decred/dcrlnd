package chainreg

import (
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainntnfs/dcrdnotify"
	"github.com/decred/dcrlnd/chainntnfs/dcrwnotify"
	"github.com/decred/dcrlnd/chainntnfs/remotedcrwnotify"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/htlcswitch"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/lncfg"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/routing/chainview"
	"github.com/decred/dcrlnd/walletunlocker"
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

	// FullDB is the full DB (the parent of ChanStateDB).
	FullDB *channeldb.DB

	// HeightHintDB is a pointer to the database that stores the height
	// hints.
	HeightHintDB kvdb.Backend

	// ChanStateDB is a pointer to the database that stores the channel
	// state.
	ChanStateDB *channeldb.ChannelStateDB

	// BlockCache is the main cache for storing block information.
	BlockCache *blockcache.BlockCache

	// WalletUnlockParams are the parameters that were used for unlocking
	// the main wallet.
	WalletUnlockParams *walletunlocker.WalletUnlockParams

	// ActiveNetParams details the current chain we are on.
	ActiveNetParams DecredNetParams

	// FeeURL defines the URL for fee estimation we will use. This field is
	// optional.
	FeeURL string

	// Dialer is a function closure that will be used to establish outbound
	// TCP connections to chain network peers in the event of a pruned block being
	// requested.
	Dialer func(string) (net.Conn, error)
}

const (
	// DefaultDecredMinHTLCInMAtoms is the default smallest value htlc this
	// node will accept. This value is proposed in the channel open
	// sequence and cannot be changed during the life of the channel.
	//
	// All forwarded payments are subjected to the min htlc constraint of
	// the routing policy of the outgoing channel. This implicitly controls
	// the minimum htlc value on the incoming channel too.
	DefaultDecredMinHTLCInMAtoms = lnwire.MilliAtom(1000)

	// DefaultDecredMinHTLCOutMAtoms is the default minimum htlc value that
	// we require for sending out htlcs. Our channel peer may have a lower
	// min htlc channel parameter, but we - by default - don't forward
	// anything under the value defined here.
	DefaultDecredMinHTLCOutMAtoms = lnwire.MilliAtom(1000)

	// DefaultDecredBaseFeeMAtoms is the default forwarding base fee.
	DefaultDecredBaseFeeMAtoms = lnwire.MilliAtom(1000)

	// DefaultDecredFeeRate is the default forwarding fee rate.
	DefaultDecredFeeRate = lnwire.MilliAtom(1)

	// DefaultDecredTimeLockDelta is the default forwarding time lock
	// delta.
	DefaultDecredTimeLockDelta = 80

	// DefaultDecredStaticFeePerKB is the fee rate of 10000 atom/kB
	DefaultDecredStaticFeePerKB = chainfee.AtomPerKByte(1e4)

	// DefaultDecredStaticMinRelayFeeRate is the min relay fee used for
	// static estimators.
	DefaultDecredStaticMinRelayFeeRate = chainfee.FeePerKBFloor
)

// PartialChainControl contains all the primary interfaces of the chain control
// that can be purely constructed from the global configuration. No wallet
// instance is required for constructing this partial state.
type PartialChainControl struct {
	// Cfg is the configuration that was used to create the partial chain
	// control.
	Cfg *Config

	// HealthCheck is a function which can be used to send a low-cost, fast
	// query to the chain backend to ensure we still have access to our
	// node.
	HealthCheck func() error

	// FeeEstimator is used to estimate an optimal fee for transactions
	// important to us.
	FeeEstimator chainfee.Estimator

	// ChainNotifier is used to receive blockchain events that we are
	// interested in.
	ChainNotifier chainntnfs.ChainNotifier

	// ChainView is used in the router for maintaining an up-to-date graph.
	ChainView chainview.FilteredChainView

	// RoutingPolicy is the routing policy we have decided to use.
	RoutingPolicy htlcswitch.ForwardingPolicy

	// MinHtlcIn is the minimum HTLC we will accept.
	MinHtlcIn lnwire.MilliAtom

	// ChannelConstraints is the set of default constraints that will be
	// used for any incoming or outgoing channel reservation requests.
	ChannelConstraints channeldb.ChannelConstraints

	// RPCConfig is the config to the remote dcrd instance when using a
	// sync mode that requires it.
	RPCConfig *rpcclient.ConnConfig
}

// ChainControl couples the three primary interfaces lnd utilizes for a
// particular chain together. A single ChainControl instance will exist for all
// the chains lnd is currently active on.
type ChainControl struct {
	// PartialChainControl is the part of the chain control that was
	// initialized purely from the configuration and doesn't contain any
	// wallet related elements.
	*PartialChainControl

	// ChainIO represents an abstraction over a source that can query the
	// blockchain.
	ChainIO lnwallet.BlockChainIO

	// Signer is used to provide signatures over things like transactions.
	Signer input.Signer

	// KeyRing represents a set of keys that we have the private keys to.
	KeyRing keychain.SecretKeyRing

	// Wc is an abstraction over some basic wallet commands. This base set
	// of commands will be provided to the Wallet *LightningWallet raw
	// pointer below.
	Wc lnwallet.WalletController

	// MsgSigner is used to sign arbitrary messages.
	MsgSigner lnwallet.MessageSigner

	// Wallet is our LightningWallet that also contains the abstract Wc
	// above. This wallet handles all of the lightning operations.
	Wallet *lnwallet.LightningWallet
}

// GenDefaultDcrChannelConstraints generates the default set of channel
// constraints that are to be used when funding a Decred channel.
func GenDefaultDcrConstraints() channeldb.ChannelConstraints {
	dustLimit := lnwallet.DustLimitForSize(input.P2PKHPkScriptSize)

	return channeldb.ChannelConstraints{
		DustLimit:        dustLimit,
		MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
	}
}

// NewPartialChainControl creates a new partial chain control that contains all
// the parts that can be purely constructed from the passed in global
// configuration and doesn't need any wallet instance yet.
func NewPartialChainControl(cfg *Config) (*PartialChainControl, func(), error) {
	// Set the RPC config from the "home" chain. Multi-chain isn't yet
	// active, so we'll restrict usage to a particular chain for now.
	log.Infof("Primary chain is set to: %v", cfg.PrimaryChain())

	cc := &PartialChainControl{
		Cfg: cfg,
	}

	switch cfg.PrimaryChain() {
	case DecredChain:
		cc.RoutingPolicy = htlcswitch.ForwardingPolicy{
			MinHTLCOut:    cfg.Decred.MinHTLCOut,
			BaseFee:       cfg.Decred.BaseFee,
			FeeRate:       cfg.Decred.FeeRate,
			TimeLockDelta: cfg.Decred.TimeLockDelta,
		}
		cc.MinHtlcIn = cfg.Decred.MinHTLCIn
		cc.FeeEstimator = chainfee.NewStaticEstimator(
			DefaultDecredStaticFeePerKB,
			DefaultDecredStaticMinRelayFeeRate,
		)
	default:
		return nil, nil, fmt.Errorf("default routing policy for chain "+
			"%v is unknown", cfg.PrimaryChain())
	}

	var err error
	heightHintCacheConfig := chainntnfs.CacheConfig{
		QueryDisable: cfg.HeightHintCacheQueryDisable,
	}
	if cfg.HeightHintCacheQueryDisable {
		log.Infof("Height Hint Cache Queries disabled")
	}

	// Initialize the height hint cache within the chain directory.
	hintCache, err := chainntnfs.NewHeightHintCache(
		heightHintCacheConfig, cfg.HeightHintDB,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to initialize height hint "+
			"cache: %v", err)
	}

	// When running in remote wallet mode, we only support running in dcrw
	// mode (using the wallet for chain operations).
	walletConn := cfg.WalletUnlockParams.Conn
	wallet := cfg.WalletUnlockParams.Wallet
	if walletConn != nil && cfg.Decred.Node != "dcrw" {
		return nil, nil, fmt.Errorf("remote wallet mode only supports " +
			"'node=dcrw' config")
	}

	// When running in embedded wallet mode with spv on, we only support
	// running in dcrw mode.
	if walletConn == nil && cfg.DcrwMode.SPV && cfg.Decred.Node != "dcrw" {
		return nil, nil, fmt.Errorf("embedded wallet in SPV mode only " +
			"supports 'node=dcrw' config")
	}

	// We only require a dcrd connection when running in embedded mode and
	// not in SPV mode.
	needsDcrd := walletConn == nil && !cfg.DcrwMode.SPV
	if needsDcrd {
		// Load dcrd's TLS cert for the RPC connection.  If a raw cert
		// was specified in the config, then we'll set that directly.
		// Otherwise, we attempt to read the cert from the path
		// specified in the config.
		dcrdMode := cfg.DcrdMode
		var rpcCert []byte
		if dcrdMode.RawRPCCert != "" {
			rpcCert, err = hex.DecodeString(dcrdMode.RawRPCCert)
			if err != nil {
				return nil, nil, err
			}
		} else {
			certFile, err := os.Open(dcrdMode.RPCCert)
			if err != nil {
				return nil, nil, err
			}
			rpcCert, err = ioutil.ReadAll(certFile)
			if err != nil {
				return nil, nil, err
			}
			if err := certFile.Close(); err != nil {
				return nil, nil, err
			}
		}

		// If the specified host for the dcrd RPC server already
		// has a port specified, then we use that directly. Otherwise,
		// we assume the default port according to the selected chain
		// parameters.
		var dcrdHost string
		if strings.Contains(dcrdMode.RPCHost, ":") {
			dcrdHost = dcrdMode.RPCHost
		} else {
			dcrdHost = fmt.Sprintf("%v:%v", dcrdMode.RPCHost,
				cfg.ActiveNetParams.RPCPort)
		}

		dcrdUser := dcrdMode.RPCUser
		dcrdPass := dcrdMode.RPCPass
		cc.RPCConfig = &rpcclient.ConnConfig{
			Host:                 dcrdHost,
			Endpoint:             "ws",
			User:                 dcrdUser,
			Pass:                 dcrdPass,
			Certificates:         rpcCert,
			DisableTLS:           false,
			DisableConnectOnNew:  true,
			DisableAutoReconnect: false,
		}

		// Verify that the provided dcrd instance exists, is reachable,
		// it's on the correct network and has the features required
		// for dcrlnd to perform its work.
		if err = checkDcrdNode(cfg.ActiveNetParams.Net, *cc.RPCConfig); err != nil {
			log.Errorf("unable to use specified dcrd node: %v",
				err)
			return nil, nil, err
		}
	}

	// Initialize the appopriate wallet controller (either the embedded
	// dcrwallet or a remote one).
	switch {
	case walletConn != nil:
		log.Info("Using the remote wallet for chain operations")

		// Remote wallet mode currently always use the wallet for chain
		// notifications and chain IO.
		cc.ChainNotifier, err = remotedcrwnotify.New(
			walletConn, cfg.ActiveNetParams.Params, hintCache,
			hintCache, cfg.BlockCache,
		)
		if err != nil {
			return nil, nil, err
		}

		cc.ChainView, err = chainview.NewRemoteWalletFilteredChainView(walletConn,
			cfg.BlockCache)
		if err != nil {
			log.Errorf("unable to create chain view: %v", err)
			return nil, nil, err
		}

	case cfg.Decred.Node == "dcrw":
		// Use the wallet itself for chain IO.
		log.Info("Using the wallet for chain operations")

		cc.ChainNotifier, err = dcrwnotify.New(
			wallet, cfg.ActiveNetParams.Params, hintCache,
			hintCache, cfg.BlockCache,
		)
		if err != nil {
			return nil, nil, err
		}

		cc.ChainView, err = chainview.NewDcrwalletFilteredChainView(
			wallet, cfg.BlockCache,
		)
		if err != nil {
			log.Errorf("unable to create chain view: %v", err)
			return nil, nil, err
		}

	case cfg.Decred.Node == "dcrd":
		// Use the dcrd node for chain IO.
		log.Info("Using dcrd for chain operations")

		cc.ChainNotifier, err = dcrdnotify.New(
			cc.RPCConfig, cfg.ActiveNetParams.Params, hintCache,
			hintCache, cfg.BlockCache,
		)
		if err != nil {
			return nil, nil, err
		}

		// Finally, we'll create an instance of the default
		// chain view to be used within the routing layer.
		cc.ChainView, err = chainview.NewDcrdFilteredChainView(*cc.RPCConfig)
		if err != nil {
			log.Errorf("unable to create chain view: %v", err)
			return nil, nil, err
		}

		// If we're not in simnet or regtest mode, then we'll
		// attempt to use a proper fee estimator.
		if !cfg.Decred.SimNet && !cfg.Decred.RegTest {
			log.Infof("Initializing dcrd backed fee estimator")

			// Finally, we'll re-initialize the fee estimator, as
			// if we're using dcrd as a backend, then we can use
			// live fee estimates, rather than a statically coded
			// value.
			//
			// TODO(decred) Review if fallbackFeeRate should be higher than
			// the default relay fee.
			fallBackFeeRate := chainfee.AtomPerKByte(1e4)
			cc.FeeEstimator, err = chainfee.NewDcrdEstimator(
				*cc.RPCConfig, fallBackFeeRate,
			)
			if err != nil {
				return nil, nil, err
			}
		}

	case cfg.Decred.Node == "nochainbackend":
		backend := &NoChainBackend{}

		cc.ChainNotifier = backend
		cc.ChainView = backend
		cc.FeeEstimator = backend

		cc.HealthCheck = func() error {
			return nil
		}

	default:
		return nil, nil, fmt.Errorf("unknown sync mode")
	}

	// Override default fee estimator if an external service is specified.
	if cfg.FeeURL != "" {
		// Do not cache fees on regtest and simnet to make it easier to
		// execute manual or automated test cases.
		cacheFees := !cfg.Decred.RegTest && !cfg.Decred.SimNet

		log.Infof("Using external fee estimator %v: cached=%v",
			cfg.FeeURL, cacheFees)

		cc.FeeEstimator = chainfee.NewWebAPIEstimator(
			chainfee.SparseConfFeeSource{
				URL: cfg.FeeURL,
			},
			!cacheFees,
		)
	}

	ccCleanup := func() {
		if cc.FeeEstimator != nil {
			if err := cc.FeeEstimator.Stop(); err != nil {
				log.Errorf("Failed to stop feeEstimator: %v",
					err)
			}
		}
	}

	// Start fee estimator.
	if err := cc.FeeEstimator.Start(); err != nil {
		return nil, nil, err
	}

	// Select the default channel constraints for the primary chain.
	cc.ChannelConstraints = GenDefaultDcrConstraints()

	return cc, ccCleanup, nil
}

// NewChainControl attempts to create a ChainControl instance according
// to the parameters in the passed configuration. Currently three
// branches of ChainControl instances exist: one backed by a running btcd
// full-node, another backed by a running bitcoind full-node, and the other
// backed by a running neutrino light client instance. When running with a
// neutrino light client instance, `neutrinoCS` must be non-nil.
func NewChainControl(walletConfig lnwallet.Config,
	msgSigner lnwallet.MessageSigner,
	pcc *PartialChainControl) (*ChainControl, func(), error) {

	cc := &ChainControl{
		PartialChainControl: pcc,
		MsgSigner:           msgSigner,
		Signer:              walletConfig.Signer,
		ChainIO:             walletConfig.ChainIO,
		Wc:                  walletConfig.WalletController,
		KeyRing:             walletConfig.SecretKeyRing,
	}

	ccCleanup := func() {
		if cc.Wallet != nil {
			if err := cc.Wallet.Shutdown(); err != nil {
				log.Errorf("Failed to shutdown wallet: %v", err)
			}
		}
	}

	// Set the chain IO healthcheck.
	//
	// NOTE: in lnd this is done in NewPartialChainControl().
	pcc.HealthCheck = func() error {
		_, _, err := cc.ChainIO.GetBestBlock()
		return err
	}

	lnWallet, err := lnwallet.NewLightningWallet(walletConfig)
	if err != nil {
		err := fmt.Errorf("unable to create wallet: %v", err)
		return nil, ccCleanup, err
	}
	if err := lnWallet.Startup(); err != nil {
		err := fmt.Errorf("unable to start wallet: %v", err)
		return nil, ccCleanup, err
	}

	log.Info("LightningWallet opened")
	cc.Wallet = lnWallet

	return cc, ccCleanup, nil
}

var (
	// decredTestnet3Genesis is the genesis hash of Decred's testnet3
	// chain.
	decredTestnet3Genesis = chainhash.Hash([chainhash.HashSize]byte{
		0xac, 0x9b, 0xa4, 0x34, 0xb6, 0xf7, 0x24, 0x9b,
		0x96, 0x98, 0xd1, 0xfc, 0xec, 0x26, 0xd6, 0x08,
		0x7e, 0x83, 0x58, 0xc8, 0x11, 0xc7, 0xe9, 0x22,
		0xf4, 0xca, 0x18, 0x39, 0xe5, 0xdc, 0x49, 0xa6,
	})

	// decredMainnetGenesis is the genesis hash of Decred's main chain.
	decredMainnetGenesis = chainhash.Hash([chainhash.HashSize]byte{
		0x80, 0xd9, 0x21, 0x2b, 0xf4, 0xce, 0xb0, 0x66,
		0xde, 0xd2, 0x86, 0x6b, 0x39, 0xd4, 0xed, 0x89,
		0xe0, 0xab, 0x60, 0xf3, 0x35, 0xc1, 0x1d, 0xf8,
		0xe7, 0xbf, 0x85, 0xd9, 0xc3, 0x5c, 0x8e, 0x29,
	})

	// chainMap is a simple index that maps a chain's genesis hash to the
	// ChainCode enum for that chain.
	chainMap = map[chainhash.Hash]ChainCode{
		decredTestnet3Genesis: DecredChain,

		decredMainnetGenesis: DecredChain,
	}

	// ChainDNSSeeds is a map of a chain's hash to the set of DNS seeds
	// that will be use to bootstrap peers upon first startup.
	//
	// The first item in the array is the primary host we'll use to attempt
	// the SRV lookup we require. If we're unable to receive a response
	// over UDP, then we'll fall back to manual TCP resolution. The second
	// item in the array is a special A record that we'll query in order to
	// receive the IP address of the current authoritative DNS server for
	// the network seed.
	//
	// TODO(roasbeef): extend and collapse these and chainparams.go into
	// struct like chaincfg.Params
	ChainDNSSeeds = map[chainhash.Hash][][2]string{
		// TODO(decred): Add actual decred DNS seeder addresses once
		// they're up.
		decredMainnetGenesis:  nil,
		decredTestnet3Genesis: nil,
	}
)

// ChainRegistry keeps track of the current chains
type ChainRegistry struct {
	sync.RWMutex

	activeChains map[ChainCode]*ChainControl
	netParams    map[ChainCode]*DecredNetParams

	primaryChain ChainCode
}

// NewChainRegistry creates a new ChainRegistry.
func NewChainRegistry() *ChainRegistry {
	return &ChainRegistry{
		activeChains: make(map[ChainCode]*ChainControl),
		netParams:    make(map[ChainCode]*DecredNetParams),
	}
}

// RegisterChain assigns an active ChainControl instance to a target chain
// identified by its ChainCode.
func (c *ChainRegistry) RegisterChain(newChain ChainCode, cc *ChainControl) {
	c.Lock()
	c.activeChains[newChain] = cc
	c.Unlock()
}

// LookupChain attempts to lookup an active ChainControl instance for the
// target chain.
func (c *ChainRegistry) LookupChain(targetChain ChainCode) (*ChainControl, bool) {
	c.RLock()
	cc, ok := c.activeChains[targetChain]
	c.RUnlock()
	return cc, ok
}

// LookupChainByHash attempts to look up an active ChainControl which
// corresponds to the passed genesis hash.
func (c *ChainRegistry) LookupChainByHash(
	chainHash chainhash.Hash) (*ChainControl, bool) {

	c.RLock()
	defer c.RUnlock()

	targetChain, ok := chainMap[chainHash]
	if !ok {
		return nil, ok
	}

	cc, ok := c.activeChains[targetChain]
	return cc, ok
}

// RegisterPrimaryChain sets a target chain as the "home chain" for lnd.
func (c *ChainRegistry) RegisterPrimaryChain(cc ChainCode) {
	c.Lock()
	defer c.Unlock()

	c.primaryChain = cc
}

// PrimaryChain returns the primary chain for this running lnd instance. The
// primary chain is considered the "home base" while the other registered
// chains are treated as secondary chains.
func (c *ChainRegistry) PrimaryChain() ChainCode {
	c.RLock()
	defer c.RUnlock()

	return c.primaryChain
}

// ActiveChains returns a slice containing the active chains.
func (c *ChainRegistry) ActiveChains() []ChainCode {
	c.RLock()
	defer c.RUnlock()

	chains := make([]ChainCode, 0, len(c.activeChains))
	for activeChain := range c.activeChains {
		chains = append(chains, activeChain)
	}

	return chains
}

// NumActiveChains returns the total number of active chains.
func (c *ChainRegistry) NumActiveChains() uint32 {
	c.RLock()
	defer c.RUnlock()

	return uint32(len(c.activeChains))
}
