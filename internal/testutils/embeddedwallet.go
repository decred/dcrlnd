package testutils

import (
	"context"
	"errors"
	"io/ioutil"
	"net"
	"os"
	"time"

	"decred.org/dcrwallet/v3/chain"
	"decred.org/dcrwallet/v3/p2p"
	"decred.org/dcrwallet/v3/spv"
	wallet "decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrd/addrmgr/v2"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrlnd/build"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
)

var (
	testHDSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x98, 0xa3, 0xef, 0xb9,
		0x69, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}
)

func init() {
	wallet.UseLogger(build.NewSubLogger("DCRW", nil))
	p2p.UseLogger(build.NewSubLogger("DCRW", nil))
	spv.UseLogger(build.NewSubLogger("DCRW", nil))
}

// NewRPCSyncingTestWallet creates a test wallet that syncs itself using the
// RPC connection mode.
func NewRPCSyncingTestWallet(t TB, rpcConfig *rpcclient.ConnConfig) (*wallet.Wallet, func()) {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "test-dcrw-rpc")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if t.Failed() {
			t.Logf("Wallet data at %s", tempDir)
		}
	}()

	loader := walletloader.NewLoader(chaincfg.SimNetParams(), tempDir,
		wallet.DefaultGapLimit)

	pass := []byte("test")

	w, err := loader.CreateNewWallet(
		context.Background(), pass, pass, testHDSeed[:],
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Unlock(context.Background(), pass, nil); err != nil {
		t.Fatal(err)
	}

	chainRpcOpts := chain.RPCOptions{
		Address: rpcConfig.Host,
		User:    rpcConfig.User,
		Pass:    rpcConfig.Pass,
		CA:      rpcConfig.Certificates,
	}
	syncer := chain.NewSyncer(w, &chainRpcOpts)
	syncerCtx, cancel := context.WithCancel(context.Background())
	initialSync := make(chan struct{})
	syncer.SetCallbacks(&chain.Callbacks{
		Synced: func(_ bool) {
			select {
			case <-initialSync:
			default:
				close(initialSync)
			}
		},
	})
	go syncer.Run(syncerCtx)

	select {
	case <-initialSync:
	case <-time.After(60 * time.Second):
		t.Fatal("timeout waiting for initial wallet sync")
	}

	cleanUp := func() {
		cancel()
		w.Lock()
		if !t.Failed() {
			os.RemoveAll(tempDir)
		} else {
			t.Logf("Wallet data at %s", tempDir)
		}
	}

	return w, cleanUp
}

// NewSPVSyncingTestWallet creates a test wallet that syncs itself using the
// SPV connection mode.
func NewSPVSyncingTestWallet(t TB, p2pAddr string) (*wallet.Wallet, func()) {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "test-dcrw-spv")
	if err != nil {
		t.Fatal(err)
	}

	defer func() {
		if t.Failed() {
			t.Logf("Wallet data at %s", tempDir)
		}
	}()

	loader := walletloader.NewLoader(chaincfg.SimNetParams(), tempDir,
		wallet.DefaultGapLimit)

	pass := []byte("test")

	w, err := loader.CreateNewWallet(
		context.Background(), pass, pass, testHDSeed[:],
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Unlock(context.Background(), pass, nil); err != nil {
		t.Fatal(err)
	}

	addr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}
	amgrDir := tempDir
	amgr := addrmgr.New(amgrDir, net.LookupIP)
	lp := p2p.NewLocalPeer(w.ChainParams(), addr, amgr)
	syncer := spv.NewSyncer(w, lp)
	syncer.SetPersistentPeers([]string{p2pAddr})
	w.SetNetworkBackend(syncer)
	ctx, cancel := context.WithCancel(context.Background())
	syncerChan := make(chan error, 1)
	go func() { syncerChan <- syncer.Run(ctx) }()

	cleanUp := func() {
		cancel()
		select {
		case err := <-syncerChan:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Logf("SPV syncer errored: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Logf("Timeout waiting for SPV syncer to finish")
		}
		w.Lock()
		if !t.Failed() {
			os.RemoveAll(tempDir)
		} else {
			t.Logf("Wallet data at %s", tempDir)
		}
	}

	return w, cleanUp
}
