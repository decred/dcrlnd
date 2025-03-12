package dcrwallet

import (
	"context"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/decred/dcrd/addrmgr/v2"
	"github.com/decred/dcrd/chaincfg/v3"

	"decred.org/dcrwallet/v4/p2p"
	"decred.org/dcrwallet/v4/spv"
)

type SPVSyncerConfig struct {
	Peers          []string
	Net            *chaincfg.Params
	AppDataDir     string
	DialFunc       p2p.DialFunc
	DisableRelayTx bool
}

// SPVSyncer implements the required methods for synchronizing a DcrWallet
// instance using the SPV method.
type SPVSyncer struct {
	cfg *SPVSyncerConfig
	wg  sync.WaitGroup

	// runCtx is canceled when Stop() is called.
	runCtx    context.Context
	runCancel func()
}

// NewSPVSyncer initializes a new syncer backed by the dcrd network in SPV
// mode.
func NewSPVSyncer(cfg *SPVSyncerConfig) (*SPVSyncer, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &SPVSyncer{
		cfg:       cfg,
		runCtx:    ctx,
		runCancel: cancel,
	}, nil
}

// start the syncer backend and begin synchronizing the given wallet.
func (s *SPVSyncer) start(w *DcrWallet) error {

	lookup := net.LookupIP

	disableDiscoverAccts, err := w.cfg.DB.AccountDiscoveryDisabled()
	if err != nil {
		return err
	}

	addr := &net.TCPAddr{IP: net.ParseIP("::1"), Port: 0}
	amgrDir := filepath.Join(s.cfg.AppDataDir, s.cfg.Net.Name)
	amgr := addrmgr.New(amgrDir, lookup)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			dcrwLog.Debugf("Starting SPV syncer")
			lp := p2p.NewLocalPeer(s.cfg.Net, addr, amgr)
			if s.cfg.DialFunc != nil {
				lp.SetDialFunc(s.cfg.DialFunc)
			}
			lp.SetDisableRelayTx(s.cfg.DisableRelayTx)

			syncer := spv.NewSyncer(w.wallet, lp)
			if len(s.cfg.Peers) > 0 {
				dcrwLog.Debugf("Forcing SPV to peers: %s", s.cfg.Peers)
				syncer.SetPersistentPeers(s.cfg.Peers)
			}

			if disableDiscoverAccts {
				syncer.DisableDiscoverAccounts()
			}

			syncer.SetNotifications(&spv.Notifications{
				Synced: w.onSyncerSynced,
			})

			err := syncer.Run(s.runCtx)
			if err == nil || s.runCtx.Err() != nil {
				// stop() requested.
				return
			}

			w.rpcSyncerFinished()
			dcrwLog.Errorf("SPV synchronization ended: %v", err)

			// Backoff for 5 seconds.
			select {
			case <-s.runCtx.Done():
				// Graceful shutdown.
				dcrwLog.Debugf("RPCsyncer shutting down")
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()

	return nil
}

func (s *SPVSyncer) stop() {
	dcrwLog.Debugf("SPVSyncer requested shutdown")
	s.runCancel()
}

func (s *SPVSyncer) waitForShutdown() {
	s.wg.Wait()
}
