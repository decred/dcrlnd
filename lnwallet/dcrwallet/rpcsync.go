package dcrwallet

import (
	"context"
	"os"
	"sync"
	"time"

	"decred.org/dcrwallet/v4/chain"
	"decred.org/dcrwallet/v4/errors"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpcclient/v8"
)

// RPCSyncer implements the required methods for synchronizing a DcrWallet
// instance using a full node dcrd backend.
type RPCSyncer struct {
	rpcConfig rpcclient.ConnConfig
	net       *chaincfg.Params
	wg        sync.WaitGroup

	mtx sync.Mutex

	// The following fields are protected by mtx.

	cancel func()
}

// NewRPCSyncer initializes a new syncer backed by a full dcrd node. It
// requires the config for reaching the dcrd instance and the corresponding
// network this instance should be in.
func NewRPCSyncer(rpcConfig rpcclient.ConnConfig, net *chaincfg.Params) (*RPCSyncer, error) {
	return &RPCSyncer{
		rpcConfig: rpcConfig,
		net:       net,
	}, nil
}

// start the syncer backend and begin synchronizing the given wallet.
func (s *RPCSyncer) start(w *DcrWallet) error {
	var clientCert, clientKey []byte
	var err error

	if s.rpcConfig.ClientCert != "" {
		clientCert, err = os.ReadFile(s.rpcConfig.ClientCert)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if s.rpcConfig.ClientKey != "" {
		clientKey, err = os.ReadFile(s.rpcConfig.ClientKey)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	chainRpcOpts := chain.RPCOptions{
		Address:    s.rpcConfig.Host,
		User:       s.rpcConfig.User,
		Pass:       s.rpcConfig.Pass,
		CA:         s.rpcConfig.Certificates,
		ClientCert: clientCert,
		ClientKey:  clientKey,
	}

	disableDiscoverAccts, err := w.cfg.DB.AccountDiscoveryDisabled()
	if err != nil {
		return err
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			// This context will be canceled by `w` once its Stop() method is
			// called.
			ctx, cancel := context.WithCancel(context.Background())
			s.mtx.Lock()
			s.cancel = cancel
			s.mtx.Unlock()

			syncer := chain.NewSyncer(w.wallet, &chainRpcOpts)
			syncer.SetCallbacks(&chain.Callbacks{
				Synced: w.onSyncerSynced,
			})

			if disableDiscoverAccts {
				syncer.DisableDiscoverAccounts()
			}

			dcrwLog.Debugf("Starting rpc syncer")
			err := syncer.Run(ctx)
			w.rpcSyncerFinished()

			// TODO: convert to errors.Is
			if werr, is := err.(*errors.Error); is && werr.Err == context.Canceled {
				// This was a graceful shutdown, so ignore the error.
				dcrwLog.Debugf("RPCsyncer shutting down")
				return
			}
			dcrwLog.Errorf("RPCSyncer error: %v", err)

			// Backoff for 5 seconds.
			select {
			case <-ctx.Done():
				// Graceful shutdown.
				dcrwLog.Debugf("RPCsyncer shutting down")
				return
			case <-time.After(5 * time.Second):
			}

			// Clear and call s.cancel() so we don't leak it.
			s.mtx.Lock()
			s.cancel = nil
			s.mtx.Unlock()
			cancel()
		}
	}()

	return nil
}

func (s *RPCSyncer) stop() {
	dcrwLog.Debugf("RPCSyncer requested shutdown")
	s.mtx.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.mtx.Unlock()
}

func (s *RPCSyncer) waitForShutdown() {
	s.wg.Wait()
}
