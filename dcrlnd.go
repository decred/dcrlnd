package dcrlnd

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"

	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/lnrpc/initchainsyncrpc"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	walletloader "github.com/decred/dcrlnd/lnwallet/dcrwallet/loader"
	"github.com/decred/dcrlnd/lnwallet/remotedcrwallet"
	"github.com/decred/dcrlnd/signal"
	"github.com/decred/slog"
	"gopkg.in/macaroon-bakery.v2/bakery"
)

var errShutdownRequested = errors.New("shutdown requested")

var initChainSyncPermissions = map[string][]bakery.Op{
	"/initialchainsyncrpc.InitialChainSync/SubscribeChainSync": {{
		Entity: "onchain",
		Action: "read",
	}},
}

// waitForInitialChainSync waits until the initial chain sync is completed
// before returning. It creates a gRPC service to listen to requests to provide
// the sync progress.
func waitForInitialChainSync(activeChainControl *chainreg.ChainControl,
	interceptor *signal.Interceptor, svc *initchainsyncrpc.Server) error {

	_, bestHeight, err := activeChainControl.ChainIO.GetBestBlock()
	if err != nil {
		return err
	}
	ltndLog.Infof("Waiting for chain backend to finish sync, "+
		"start_height=%v", bestHeight)

	svc.SetChainControl(activeChainControl.Wallet)

	// Wait until the initial sync is done.
	select {
	case <-interceptor.ShutdownChannel():
		return errShutdownRequested
	case <-activeChainControl.Wallet.InitialSyncChannel():
	}

	_, bestHeight, err = activeChainControl.ChainIO.GetBestBlock()
	if err != nil {
		return err
	}
	ltndLog.Infof("Chain backend is fully synced (end_height=%v)!",
		bestHeight)

	return nil
}

func noSeedBackupWalletInit(ctx context.Context, cfg *Config, privPass, pubPass []byte) (*wallet.Wallet, error) {

	netDir := dcrwallet.NetworkDir(
		cfg.Decred.ChainDir, cfg.ActiveNetParams.Params,
	)
	loader := walletloader.NewLoader(
		cfg.ActiveNetParams.Params, netDir, wallet.DefaultGapLimit,
		// loaderOpts...,
	)
	exists, err := loader.WalletExists()
	if err != nil {
		return nil, err
	}
	if exists {
		return loader.OpenExistingWallet(ctx, pubPass)
	}

	var seed [32]byte
	if _, err := io.ReadFull(rand.Reader, seed[:]); err != nil {
		return nil, err
	}
	return loader.CreateNewWallet(ctx, pubPass, privPass, seed[:])
}

type RemoteWalletBuilder struct {
	logger slog.Logger
}

func (rb *RemoteWalletBuilder) BuildChainControl(
	partialChainControl *chainreg.PartialChainControl,
	walletConfig *chainreg.WalletConfig) (*chainreg.ChainControl, func(), error) {

	cfg := partialChainControl.Cfg

	// Initialize an RPC syncer for this wallet and use it as
	// blockchain IO source.
	dcrwConfig := &remotedcrwallet.Config{
		PrivatePass:   walletConfig.PrivatePass,
		NetParams:     cfg.ActiveNetParams.Params,
		DB:            cfg.FullDB,
		Conn:          partialChainControl.Cfg.WalletUnlockParams.Conn,
		AccountNumber: walletConfig.AccountNb,
		BlockCache:    cfg.BlockCache,
	}

	walletController, err := remotedcrwallet.New(*dcrwConfig)
	if err != nil {
		err := fmt.Errorf("unable to create remote wallet "+
			"controller: %v\n", err)
		return nil, nil, err
	}

	// Create, and start the lnwallet, which handles the core payment
	// channel logic, and exposes control via proxy state machines.
	lnWalletConfig := lnwallet.Config{
		Database:           partialChainControl.Cfg.ChanStateDB,
		Notifier:           partialChainControl.ChainNotifier,
		WalletController:   walletController,
		Signer:             walletController,
		FeeEstimator:       partialChainControl.FeeEstimator,
		SecretKeyRing:      walletController,
		ChainIO:            walletController,
		DefaultConstraints: partialChainControl.ChannelConstraints,
		NetParams:          *partialChainControl.Cfg.ActiveNetParams.Params,
	}

	// We've created the wallet configuration now, so we can finish
	// initializing the main chain control.
	activeChainControl, cleanUp, err := chainreg.NewChainControl(
		lnWalletConfig, walletController, partialChainControl,
	)
	if err != nil {
		err := fmt.Errorf("unable to create chain control: %v", err)
		rb.logger.Error(err)
		return nil, nil, err
	}

	return activeChainControl, cleanUp, nil
}
