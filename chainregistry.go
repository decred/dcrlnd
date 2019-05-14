package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/rpcclient/v2"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainntnfs/dcrdnotify"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/htlcswitch"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/routing/chainview"
	walletloader "github.com/decred/dcrwallet/loader"
	"github.com/decred/dcrwallet/wallet/v2"
)

const (
	// TODO(decred) verify these amounts
	defaultDecredMinHTLCMAtoms = lnwire.MilliAtom(1000)
	defaultDecredBaseFeeMAtoms = lnwire.MilliAtom(1000)
	defaultDecredFeeRate       = lnwire.MilliAtom(1)
	defaultDecredTimeLockDelta = 144

	// defaultDecredStaticFeePerKB is the fee rate of 10000 atom/kB
	defaultDecredStaticFeePerKB = lnwallet.AtomPerKByte(1e4)
)

// defaultDcrChannelConstraints is the default set of channel constraints that are
// meant to be used when initially funding a Decred channel.
//
// TODO(halseth): make configurable at startup?
var defaultDcrChannelConstraints = channeldb.ChannelConstraints{
	DustLimit:        lnwallet.DefaultDustLimit(),
	MaxAcceptedHtlcs: lnwallet.MaxHTLCNumber / 2,
}

// chainCode is an enum-like structure for keeping track of the chains
// currently supported within lnd.
type chainCode uint32

const (
	// decredChain is Decred's testnet chain.
	decredChain chainCode = iota
)

// String returns a string representation of the target chainCode.
func (c chainCode) String() string {
	switch c {
	case decredChain:
		return "decred"
	default:
		return "kekcoin"
	}
}

// checkDcrdNode checks whether the dcrd node reachable using the provided
// config is usable as source of chain information, given the requirements of a
// dcrlnd node.
func checkDcrdNode(rpcConfig rpcclient.ConnConfig) error {
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
	net, err := chainConn.GetCurrentNet()
	if err != nil {
		return err
	}
	if net != activeNetParams.Params.Net {
		return fmt.Errorf("dcrd node network mismatch")
	}

	// Verify if the txindex is enabled on the node. This is currently
	// required by several dcrlnd services to perform tx discovery. Try to
	// fetch a dummy transaction and check if the error code specifies the
	// index is not enabled.
	var nullTxID chainhash.Hash
	errNoTxIndexMsg := "The transaction index must be enabled"
	_, err = chainConn.GetRawTransaction(&nullTxID)
	if err != nil && strings.Contains(err.Error(), errNoTxIndexMsg) {
		return fmt.Errorf("dcrd instance is not running with --txindex")
	}

	return nil
}

// chainControl couples the three primary interfaces lnd utilizes for a
// particular chain together. A single chainControl instance will exist for all
// the chains lnd is currently active on.
type chainControl struct {
	chainIO lnwallet.BlockChainIO

	feeEstimator lnwallet.FeeEstimator

	signer lnwallet.Signer

	keyRing keychain.KeyRing

	wc lnwallet.WalletController

	msgSigner lnwallet.MessageSigner

	chainNotifier chainntnfs.ChainNotifier

	chainView chainview.FilteredChainView

	wallet *lnwallet.LightningWallet

	routingPolicy htlcswitch.ForwardingPolicy
}

// newChainControlFromConfig attempts to create a chainControl instance
// according to the parameters in the passed lnd configuration. Currently only
// one chainControl instance exists: one backed by a running dcrd full-node.
func newChainControlFromConfig(cfg *config, chanDB *channeldb.DB,
	privateWalletPw, publicWalletPw []byte, birthday time.Time,
	recoveryWindow uint32, wallet *wallet.Wallet,
	loader *walletloader.Loader) (*chainControl, func(), error) {

	// Set the RPC config from the "home" chain. Multi-chain isn't yet
	// active, so we'll restrict usage to a particular chain for now.
	homeChainConfig := cfg.Decred
	ltndLog.Infof("Primary chain is set to: %v",
		registeredChains.PrimaryChain())

	cc := &chainControl{}

	switch registeredChains.PrimaryChain() {
	case decredChain:
		cc.routingPolicy = htlcswitch.ForwardingPolicy{
			MinHTLC:       cfg.Decred.MinHTLC,
			BaseFee:       cfg.Decred.BaseFee,
			FeeRate:       cfg.Decred.FeeRate,
			TimeLockDelta: cfg.Decred.TimeLockDelta,
		}
		cc.feeEstimator = lnwallet.NewStaticFeeEstimator(
			defaultDecredStaticFeePerKB, 0,
		)
	default:
		return nil, nil, fmt.Errorf("default routing policy for "+
			"chain %v is unknown", registeredChains.PrimaryChain())
	}

	var chainIO lnwallet.BlockChainIO

	walletConfig := &dcrwallet.Config{
		PrivatePass:    privateWalletPw,
		PublicPass:     publicWalletPw,
		Birthday:       birthday,
		RecoveryWindow: recoveryWindow,
		DataDir:        homeChainConfig.ChainDir,
		NetParams:      activeNetParams.Params,
		FeeEstimator:   cc.feeEstimator,
		Wallet:         wallet,
		Loader:         loader,
	}

	var (
		err     error
		cleanUp func()
	)

	// Initialize the height hint cache within the chain directory.
	hintCache, err := chainntnfs.NewHeightHintCache(chanDB)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to initialize height hint "+
			"cache: %v", err)
	}

	switch homeChainConfig.Node {
	case "dcrd":
		// Otherwise, we'll be speaking directly via RPC to a node.
		//
		// So first we'll load dcrd's TLS cert for the RPC
		// connection. If a raw cert was specified in the config, then
		// we'll set that directly. Otherwise, we attempt to read the
		// cert from the path specified in the config.
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
				activeNetParams.rpcPort)
		}

		dcrdUser := dcrdMode.RPCUser
		dcrdPass := dcrdMode.RPCPass
		rpcConfig := &rpcclient.ConnConfig{
			Host:                 dcrdHost,
			Endpoint:             "ws",
			User:                 dcrdUser,
			Pass:                 dcrdPass,
			Certificates:         rpcCert,
			DisableTLS:           false,
			DisableConnectOnNew:  true,
			DisableAutoReconnect: false,
		}
		cc.chainNotifier, err = dcrdnotify.New(
			rpcConfig, hintCache, hintCache,
		)
		if err != nil {
			return nil, nil, err
		}

		// Finally, we'll create an instance of the default chain view to be
		// used within the routing layer.
		cc.chainView, err = chainview.NewDcrdFilteredChainView(*rpcConfig)
		if err != nil {
			srvrLog.Errorf("unable to create chain view: %v", err)
			return nil, nil, err
		}

		// Verify that the provided dcrd instance exists, is reachable,
		// it's on the correct network and has the features required
		// for dcrlnd to perform its work.
		if err = checkDcrdNode(*rpcConfig); err != nil {
			srvrLog.Errorf("unable to use specified dcrd node: %v",
				err)
			return nil, nil, err
		}

		// Initialize an RPC syncer for this wallet and use it as
		// blockchain IO source.
		syncer, err := dcrwallet.NewRPCSyncer(*rpcConfig,
			activeNetParams.Params)
		if err != nil {
			return nil, nil, err
		}
		walletConfig.Syncer = syncer
		chainIO = syncer

		// If we're not in simnet or regtest mode, then we'll attempt
		// to use a proper fee estimator for testnet.
		if !cfg.Decred.SimNet && !cfg.Decred.RegTest {
			ltndLog.Infof("Initializing dcrd backed fee estimator")

			// Finally, we'll re-initialize the fee estimator, as
			// if we're using dcrd as a backend, then we can use
			// live fee estimates, rather than a statically coded
			// value.
			// TODO(decred) Review if fallbackFeeRate should be higher than
			// the default relay fee.
			fallBackFeeRate := lnwallet.AtomPerKByte(1e4)
			cc.feeEstimator, err = lnwallet.NewDcrdFeeEstimator(
				*rpcConfig, fallBackFeeRate,
			)
			if err != nil {
				return nil, nil, err
			}
			if err := cc.feeEstimator.Start(); err != nil {
				return nil, nil, err
			}
		}
	default:
		return nil, nil, fmt.Errorf("unknown node type: %s",
			homeChainConfig.Node)
	}

	wc, err := dcrwallet.New(*walletConfig)
	if err != nil {
		fmt.Printf("unable to create wallet controller: %v\n", err)
		return nil, nil, err
	}

	cc.msgSigner = wc
	cc.signer = wc
	cc.chainIO = chainIO
	cc.wc = wc

	// Select the default channel constraints for the primary chain.
	channelConstraints := defaultDcrChannelConstraints

	keyRing := dcrwallet.NewWalletKeyRing(
		wc.InternalWallet(),
	)
	cc.keyRing = keyRing

	// Create, and start the lnwallet, which handles the core payment
	// channel logic, and exposes control via proxy state machines.
	walletCfg := lnwallet.Config{
		Database:           chanDB,
		Notifier:           cc.chainNotifier,
		WalletController:   wc,
		Signer:             cc.signer,
		FeeEstimator:       cc.feeEstimator,
		SecretKeyRing:      keyRing,
		ChainIO:            cc.chainIO,
		DefaultConstraints: channelConstraints,
		NetParams:          *activeNetParams.Params,
	}
	lnWallet, err := lnwallet.NewLightningWallet(walletCfg)
	if err != nil {
		fmt.Printf("unable to create wallet: %v\n", err)
		return nil, nil, err
	}
	if err := lnWallet.Startup(); err != nil {
		fmt.Printf("unable to start wallet: %v\n", err)
		return nil, nil, err
	}

	ltndLog.Info("LightningWallet opened")

	cc.wallet = lnWallet

	return cc, cleanUp, nil
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
	// chainCode enum for that chain.
	chainMap = map[chainhash.Hash]chainCode{
		decredTestnet3Genesis: decredChain,

		decredMainnetGenesis: decredChain,
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
		decredMainnetGenesis: {
			{
				"dcr.nodes.lightning.directory",
				"soa.nodes.lightning.directory",
			},
		},

		decredTestnet3Genesis: {
			{
				"dcrtest.nodes.lightning.directory",
				"soa.nodes.lightning.directory",
			},
		},
	}
)

// chainRegistry keeps track of the current chains
type chainRegistry struct {
	sync.RWMutex

	activeChains map[chainCode]*chainControl
	netParams    map[chainCode]*decredNetParams

	primaryChain chainCode
}

// newChainRegistry creates a new chainRegistry.
func newChainRegistry() *chainRegistry {
	return &chainRegistry{
		activeChains: make(map[chainCode]*chainControl),
		netParams:    make(map[chainCode]*decredNetParams),
	}
}

// RegisterChain assigns an active chainControl instance to a target chain
// identified by its chainCode.
func (c *chainRegistry) RegisterChain(newChain chainCode, cc *chainControl) {
	c.Lock()
	c.activeChains[newChain] = cc
	c.Unlock()
}

// LookupChain attempts to lookup an active chainControl instance for the
// target chain.
func (c *chainRegistry) LookupChain(targetChain chainCode) (*chainControl, bool) {
	c.RLock()
	cc, ok := c.activeChains[targetChain]
	c.RUnlock()
	return cc, ok
}

// LookupChainByHash attempts to look up an active chainControl which
// corresponds to the passed genesis hash.
func (c *chainRegistry) LookupChainByHash(chainHash chainhash.Hash) (*chainControl, bool) {
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
func (c *chainRegistry) RegisterPrimaryChain(cc chainCode) {
	c.Lock()
	defer c.Unlock()

	c.primaryChain = cc
}

// PrimaryChain returns the primary chain for this running lnd instance. The
// primary chain is considered the "home base" while the other registered
// chains are treated as secondary chains.
func (c *chainRegistry) PrimaryChain() chainCode {
	c.RLock()
	defer c.RUnlock()

	return c.primaryChain
}

// ActiveChains returns a slice containing the active chains.
func (c *chainRegistry) ActiveChains() []chainCode {
	c.RLock()
	defer c.RUnlock()

	chains := make([]chainCode, 0, len(c.activeChains))
	for activeChain := range c.activeChains {
		chains = append(chains, activeChain)
	}

	return chains
}

// NumActiveChains returns the total number of active chains.
func (c *chainRegistry) NumActiveChains() uint32 {
	c.RLock()
	defer c.RUnlock()

	return uint32(len(c.activeChains))
}
