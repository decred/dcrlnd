package dcrlnd

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainntnfs/dcrdnotify"
	"github.com/decred/dcrlnd/chainntnfs/dcrwnotify"
	"github.com/decred/dcrlnd/chainntnfs/remotedcrwnotify"
	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/htlcswitch"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	"github.com/decred/dcrlnd/lnwallet/remotedcrwallet"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/routing/chainview"
)

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

// defaultDcrChannelConstraints is the default set of channel constraints that are
// meant to be used when initially funding a Decred channel.
//
// TODO(halseth): make configurable at startup?
var defaultDcrChannelConstraints = channeldb.ChannelConstraints{
	DustLimit:        lnwallet.DefaultDustLimit(),
	MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
}

// checkDcrdNode checks whether the dcrd node reachable using the provided
// config is usable as source of chain information, given the requirements of a
// dcrlnd node.
func checkDcrdNode(wantNet wire.CurrencyNet, rpcConfig rpcclient.ConnConfig) error {
	connectTimeout := 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	rpcConfig.DisableConnectOnNew = true
	rpcConfig.DisableAutoReconnect = false
	chainConn, err := rpcclient.New(&rpcConfig, nil)
	if err != nil {
		return err
	}

	// Try to connect to the given node.
	if err := chainConn.Connect(ctx, true); err != nil {
		return err
	}
	defer chainConn.Shutdown()

	// Verify whether the node is on the correct network.
	net, err := chainConn.GetCurrentNet(ctx)
	if err != nil {
		return err
	}
	if net != wantNet {
		return fmt.Errorf("dcrd node network mismatch")
	}

	return nil
}

// ChainControl couples the three primary interfaces lnd utilizes for a
// particular chain together. A single ChainControl instance will exist for all
// the chains lnd is currently active on.
type ChainControl struct {
	// ChainIO represents an abstraction over a source that can query the blockchain.
	ChainIO lnwallet.BlockChainIO

	// FeeEstimator is used to estimate an optimal fee for transactions important to us.
	FeeEstimator chainfee.Estimator

	// Signer is used to provide signatures over things like transactions.
	Signer input.Signer

	// KeyRing represents a set of keys that we have the private keys to.
	KeyRing keychain.SecretKeyRing

	// Wc is an abstraction over some basic wallet commands. This base set of commands
	// will be provided to the Wallet *LightningWallet raw pointer below.
	Wc lnwallet.WalletController

	// MsgSigner is used to sign arbitrary messages.
	MsgSigner lnwallet.MessageSigner

	// ChainNotifier is used to receive blockchain events that we are interested in.
	ChainNotifier chainntnfs.ChainNotifier

	// ChainView is used in the router for maintaining an up-to-date graph.
	ChainView chainview.FilteredChainView

	// Wallet is our LightningWallet that also contains the abstract Wc above. This wallet
	// handles all of the lightning operations.
	Wallet *lnwallet.LightningWallet

	// RoutingPolicy is the routing policy we have decided to use.
	RoutingPolicy htlcswitch.ForwardingPolicy

	// MinHtlcIn is the minimum HTLC we will accept.
	MinHtlcIn lnwire.MilliAtom
}

// NewChainControl attempts to create a ChainControl instance according to the
// parameters in the passed configuration.
func NewChainControl(cfg *chainreg.Config) (*ChainControl, error) {

	// Set the RPC config from the "home" chain. Multi-chain isn't yet
	// active, so we'll restrict usage to a particular chain for now.
	ltndLog.Infof("Primary chain is set to: %v",
		cfg.PrimaryChain())

	cc := &ChainControl{}

	switch cfg.PrimaryChain() {
	case chainreg.DecredChain:
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
		return nil, fmt.Errorf("default routing policy for chain %v is "+
			"unknown", cfg.PrimaryChain())
	}

	var (
		err       error
		rpcConfig *rpcclient.ConnConfig
	)

	heightHintCacheConfig := chainntnfs.CacheConfig{
		QueryDisable: cfg.HeightHintCacheQueryDisable,
	}
	if cfg.HeightHintCacheQueryDisable {
		ltndLog.Infof("Height Hint Cache Queries disabled")
	}

	// Initialize the height hint cache within the chain directory.
	hintCache, err := chainntnfs.NewHeightHintCache(
		heightHintCacheConfig, cfg.LocalChanDB,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize height hint "+
			"cache: %v", err)
	}

	// When running in remote wallet mode, we only support running in dcrw
	// mode (using the wallet for chain operations).
	if cfg.WalletConn != nil && cfg.Decred.Node != "dcrw" {
		return nil, fmt.Errorf("remote wallet mode only supports " +
			"'node=dcrw' config")
	}

	// When running in embedded wallet mode with spv on, we only support
	// running in dcrw mode.
	if cfg.WalletConn == nil && cfg.DcrwMode.SPV && cfg.Decred.Node != "dcrw" {
		return nil, fmt.Errorf("embedded wallet in SPV mode only " +
			"supports 'node=dcrw' config")
	}

	// We only require a dcrd connection when running in embedded mode and
	// not in SPV mode.
	needsDcrd := cfg.WalletConn == nil && !cfg.DcrwMode.SPV
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
				return nil, err
			}
		} else {
			certFile, err := os.Open(dcrdMode.RPCCert)
			if err != nil {
				return nil, err
			}
			rpcCert, err = ioutil.ReadAll(certFile)
			if err != nil {
				return nil, err
			}
			if err := certFile.Close(); err != nil {
				return nil, err
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
		rpcConfig = &rpcclient.ConnConfig{
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
		if err = checkDcrdNode(cfg.ActiveNetParams.Net, *rpcConfig); err != nil {
			srvrLog.Errorf("unable to use specified dcrd node: %v",
				err)
			return nil, err
		}
	}

	var secretKeyRing keychain.SecretKeyRing

	// Initialize the appopriate wallet controller (either the embedded
	// dcrwallet or a remote one).
	switch {
	case cfg.WalletConn != nil:
		srvrLog.Info("Using the remote wallet for chain operations")

		// Initialize an RPC syncer for this wallet and use it as
		// blockchain IO source.
		dcrwConfig := &remotedcrwallet.Config{
			PrivatePass:   cfg.PrivateWalletPw,
			NetParams:     cfg.ActiveNetParams.Params,
			DB:            cfg.RemoteChanDB,
			Conn:          cfg.WalletConn,
			AccountNumber: cfg.WalletAccountNb,
			ChainIO:       cc.ChainIO,
		}

		wc, err := remotedcrwallet.New(*dcrwConfig)
		if err != nil {
			fmt.Printf("unable to create remote wallet controller: %v\n", err)
			return nil, err
		}

		// Remote wallet mode currently always use the wallet for chain
		// notifications and chain IO.
		cc.ChainNotifier, err = remotedcrwnotify.New(
			cfg.WalletConn, cfg.ActiveNetParams.Params, hintCache, hintCache,
		)
		if err != nil {
			return nil, err
		}

		cc.ChainView, err = chainview.NewRemoteWalletFilteredChainView(cfg.WalletConn)
		if err != nil {
			srvrLog.Errorf("unable to create chain view: %v", err)
			return nil, err
		}

		secretKeyRing = wc
		cc.MsgSigner = wc
		cc.Signer = wc
		cc.Wc = wc
		cc.KeyRing = wc
		cc.ChainIO = wc

	default:
		// Initialize the appropriate syncer.
		var syncer dcrwallet.WalletSyncer
		switch cfg.DcrwMode.SPV {
		case false:
			syncer, err = dcrwallet.NewRPCSyncer(*rpcConfig,
				cfg.ActiveNetParams.Params)
		case true:
			spvCfg := &dcrwallet.SPVSyncerConfig{
				Peers:      cfg.DcrwMode.SPVConnect,
				Net:        cfg.ActiveNetParams.Params,
				AppDataDir: filepath.Join(cfg.Decred.ChainDir),
				DialFunc:   cfg.DcrwMode.DialFunc,
			}
			syncer, err = dcrwallet.NewSPVSyncer(spvCfg)
		}

		if err != nil {
			return nil, err
		}

		dcrwConfig := &dcrwallet.Config{
			Syncer:         syncer,
			ChainIO:        cc.ChainIO,
			PrivatePass:    cfg.PrivateWalletPw,
			PublicPass:     cfg.PublicWalletPw,
			Birthday:       cfg.Birthday,
			RecoveryWindow: cfg.RecoveryWindow,
			DataDir:        cfg.Decred.ChainDir,
			NetParams:      cfg.ActiveNetParams.Params,
			Wallet:         cfg.Wallet,
			Loader:         cfg.WalletLoader,
			DB:             cfg.RemoteChanDB,
		}

		wc, err := dcrwallet.New(*dcrwConfig)
		if err != nil {
			fmt.Printf("unable to create wallet controller: %v\n", err)
			return nil, err
		}

		// When running with an embedded wallet we can run in either
		// dcrw or dcrd node modes.
		switch cfg.Decred.Node {
		case "dcrw":
			// Use the wallet itself for chain IO.
			srvrLog.Info("Using the wallet for chain operations")

			cc.ChainNotifier, err = dcrwnotify.New(
				wc.InternalWallet(), cfg.ActiveNetParams.Params, hintCache, hintCache,
			)
			if err != nil {
				return nil, err
			}

			cc.ChainView, err = chainview.NewDcrwalletFilteredChainView(wc.InternalWallet())
			if err != nil {
				srvrLog.Errorf("unable to create chain view: %v", err)
				return nil, err
			}

			cc.ChainIO = wc

		case "dcrd":
			// Use the dcrd node for chain IO.
			srvrLog.Info("Using dcrd for chain operations")

			cc.ChainNotifier, err = dcrdnotify.New(
				rpcConfig, cfg.ActiveNetParams.Params, hintCache, hintCache,
			)
			if err != nil {
				return nil, err
			}

			// Finally, we'll create an instance of the default
			// chain view to be used within the routing layer.
			cc.ChainView, err = chainview.NewDcrdFilteredChainView(*rpcConfig)
			if err != nil {
				srvrLog.Errorf("unable to create chain view: %v", err)
				return nil, err
			}

			cc.ChainIO, err = dcrwallet.NewRPCChainIO(*rpcConfig, cfg.ActiveNetParams.Params)
			if err != nil {
				return nil, err
			}

			// If we're not in simnet or regtest mode, then we'll
			// attempt to use a proper fee estimator.
			if !cfg.Decred.SimNet && !cfg.Decred.RegTest {
				ltndLog.Infof("Initializing dcrd backed fee estimator")

				// Finally, we'll re-initialize the fee estimator, as
				// if we're using dcrd as a backend, then we can use
				// live fee estimates, rather than a statically coded
				// value.
				//
				// TODO(decred) Review if fallbackFeeRate should be higher than
				// the default relay fee.
				fallBackFeeRate := chainfee.AtomPerKByte(1e4)
				cc.FeeEstimator, err = chainfee.NewDcrdEstimator(
					*rpcConfig, fallBackFeeRate,
				)
				if err != nil {
					return nil, err
				}
			}
		}

		secretKeyRing = wc
		cc.MsgSigner = wc
		cc.Signer = wc
		cc.Wc = wc
		cc.KeyRing = wc
	}

	// Override default fee estimator if an external service is specified.
	if cfg.FeeURL != "" {
		// Do not cache fees on regtest and simnet to make it easier to
		// execute manual or automated test cases.
		cacheFees := !cfg.Decred.RegTest && !cfg.Decred.SimNet

		ltndLog.Infof("Using external fee estimator %v: cached=%v",
			cfg.FeeURL, cacheFees)

		cc.FeeEstimator = chainfee.NewWebAPIEstimator(
			chainfee.SparseConfFeeSource{
				URL: cfg.FeeURL,
			},
			!cacheFees,
		)
	}

	// Start the fee estimator.
	if err := cc.FeeEstimator.Start(); err != nil {
		return nil, err
	}

	// Select the default channel constraints for the primary chain.
	channelConstraints := defaultDcrChannelConstraints

	// Create, and start the lnwallet, which handles the core payment
	// channel logic, and exposes control via proxy state machines.
	walletCfg := lnwallet.Config{
		Database:           cfg.RemoteChanDB,
		Notifier:           cc.ChainNotifier,
		WalletController:   cc.Wc,
		Signer:             cc.Signer,
		FeeEstimator:       cc.FeeEstimator,
		SecretKeyRing:      secretKeyRing,
		ChainIO:            cc.ChainIO,
		DefaultConstraints: channelConstraints,
		NetParams:          *cfg.ActiveNetParams.Params,
	}
	lnWallet, err := lnwallet.NewLightningWallet(walletCfg)
	if err != nil {
		fmt.Printf("unable to create wallet: %v\n", err)
		return nil, err
	}
	if err := lnWallet.Startup(); err != nil {
		fmt.Printf("unable to start wallet: %v\n", err)
		return nil, err
	}

	ltndLog.Info("LightningWallet opened")

	cc.Wallet = lnWallet

	return cc, nil
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
	// chainreg.ChainCode enum for that chain.
	chainMap = map[chainhash.Hash]chainreg.ChainCode{
		decredTestnet3Genesis: chainreg.DecredChain,

		decredMainnetGenesis: chainreg.DecredChain,
	}

	// chainDNSSeeds is a map of a chain's hash to the set of DNS seeds
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
	chainDNSSeeds = map[chainhash.Hash][][2]string{
		// TODO(decred): Add actual decred DNS seeder addresses once
		// they're up.
		decredMainnetGenesis:  nil,
		decredTestnet3Genesis: nil,
	}
)

// ChainRegistry keeps track of the current chains
type ChainRegistry struct {
	sync.RWMutex

	activeChains map[chainreg.ChainCode]*ChainControl
	netParams    map[chainreg.ChainCode]*chainreg.DecredNetParams

	primaryChain chainreg.ChainCode
}

// newChainRegistry creates a new ChainRegistry.
func newChainRegistry() *ChainRegistry {
	return &ChainRegistry{
		activeChains: make(map[chainreg.ChainCode]*ChainControl),
		netParams:    make(map[chainreg.ChainCode]*chainreg.DecredNetParams),
	}
}

// RegisterChain assigns an active ChainControl instance to a target chain
// identified by its chainreg.ChainCode.
func (c *ChainRegistry) RegisterChain(newChain chainreg.ChainCode, cc *ChainControl) {
	c.Lock()
	c.activeChains[newChain] = cc
	c.Unlock()
}

// LookupChain attempts to lookup an active ChainControl instance for the
// target chain.
func (c *ChainRegistry) LookupChain(targetChain chainreg.ChainCode) (*ChainControl, bool) {
	c.RLock()
	cc, ok := c.activeChains[targetChain]
	c.RUnlock()
	return cc, ok
}

// LookupChainByHash attempts to look up an active ChainControl which
// corresponds to the passed genesis hash.
func (c *ChainRegistry) LookupChainByHash(chainHash chainhash.Hash) (*ChainControl, bool) {
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
func (c *ChainRegistry) RegisterPrimaryChain(cc chainreg.ChainCode) {
	c.Lock()
	defer c.Unlock()

	c.primaryChain = cc
}

// PrimaryChain returns the primary chain for this running lnd instance. The
// primary chain is considered the "home base" while the other registered
// chains are treated as secondary chains.
func (c *ChainRegistry) PrimaryChain() chainreg.ChainCode {
	c.RLock()
	defer c.RUnlock()

	return c.primaryChain
}

// ActiveChains returns a slice containing the active chains.
func (c *ChainRegistry) ActiveChains() []chainreg.ChainCode {
	c.RLock()
	defer c.RUnlock()

	chains := make([]chainreg.ChainCode, 0, len(c.activeChains))
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
