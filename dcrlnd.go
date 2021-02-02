package dcrlnd

import (
	"errors"

	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/signal"
	"google.golang.org/grpc"
)

var errShutdownRequested = errors.New("shutdown requested")

// waitForInitialChainSync waits until the initial chain sync is completed
// before returning. It creates a gRPC service to listen to requests to provide
// the sync progress.
func waitForInitialChainSync(activeChainControl *chainreg.ChainControl,
	serverOpts []grpc.ServerOption, grpcServer *grpc.Server) error {

	// TODO: FIX
	// svc := initchainsyncrpc.New(activeChainControl.Wallet)
	// initchainsyncrpc.RegisterInitialChainSyncServer(grpcServer, svc)

	// Wait until the initial sync is done.
	select {
	case <-signal.ShutdownChannel():
		return errShutdownRequested
	case <-activeChainControl.Wallet.InitialSyncChannel():
	}

	return nil
}
