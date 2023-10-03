package dcrlnd

import (
	"errors"

	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/lnrpc/initchainsyncrpc"
	"github.com/decred/dcrlnd/signal"
)

var errShutdownRequested = errors.New("shutdown requested")

// waitForInitialChainSync waits until the initial chain sync is completed
// before returning. It creates a gRPC service to listen to requests to provide
// the sync progress.
func waitForInitialChainSync(activeChainControl *chainreg.ChainControl, svc *initchainsyncrpc.Server) error {

	svc.SetChainControl(activeChainControl.Wallet)

	// Wait until the initial sync is done.
	select {
	case <-signal.ShutdownChannel():
		return errShutdownRequested
	case <-activeChainControl.Wallet.InitialSyncChannel():
	}

	return nil
}
