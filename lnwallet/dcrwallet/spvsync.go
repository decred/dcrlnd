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

	mtx sync.Mutex

	// The following fields are protected by mtx.

	cancel func()
}

// NewSPVSyncer initializes a new syncer backed by the dcrd network in SPV
// mode.
func NewSPVSyncer(cfg *SPVSyncerConfig) (*SPVSyncer, error) {
	return &SPVSyncer{
		cfg: cfg,
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
	lp := p2p.NewLocalPeer(s.cfg.Net, addr, amgr)
	if s.cfg.DialFunc != nil {
		lp.SetDialFunc(s.cfg.DialFunc)
	}
	lp.SetDisableRelayTx(s.cfg.DisableRelayTx)

	syncer := spv.NewSyncer(w.wallet, lp)
	if len(s.cfg.Peers) > 0 {
		syncer.SetPersistentPeers(s.cfg.Peers)
	}
	w.wallet.SetNetworkBackend(syncer)

	if disableDiscoverAccts {
		syncer.DisableDiscoverAccounts()
	}

	syncer.SetNotifications(&spv.Notifications{
		Synced: w.onSyncerSynced,
	})

	// This context will be canceled by `w` once its Stop() method is
	// called.
	ctx, cancel := context.WithCancel(context.Background())
	s.mtx.Lock()
	s.cancel = cancel
	s.mtx.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		for {
			dcrwLog.Debugf("Starting SPV syncer")
			if len(s.cfg.Peers) > 0 {
				dcrwLog.Debugf("Forcing SPV to peers: %s", s.cfg.Peers)
			}

			err := syncer.Run(ctx)
			select {
			case <-ctx.Done():
				// stop() requested.
				return
			default:
				w.rpcSyncerFinished()
				dcrwLog.Errorf("SPV synchronization ended: %v", err)
			}

			// Backoff for 5 seconds.
			select {
			case <-ctx.Done():
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
	s.mtx.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mtx.Unlock()
}

func (s *SPVSyncer) waitForShutdown() {
	s.wg.Wait()
}
