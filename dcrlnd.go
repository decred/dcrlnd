package dcrlnd

import (
	"errors"
	"sync"
	"time"

	"github.com/decred/dcrlnd/lnrpc/initchainsyncrpc"
	"github.com/decred/dcrlnd/signal"
	"google.golang.org/grpc"
)

var errShutdownRequested = errors.New("shutdown requested")

// waitForInitialChainSync waits until the initial chain sync is completed
// before returning. It creates a gRPC service to listen to requests to provide
// the sync progress.
func waitForInitialChainSync(activeChainControl *chainControl,
	serverOpts []grpc.ServerOption, getListeners rpcListeners) error {

	// Start a gRPC server listening for HTTP/2 connections, solely used
	// for showing the progress of the chain sync.
	listeners, cleanup, err := getListeners()
	if err != nil {
		return err
	}
	defer cleanup()

	// Set up a new ChainSyncProgressService, which will listen for requests
	// to provide the initial chain sync progress.
	grpcServer := grpc.NewServer(serverOpts...)
	defer func() {
		// Unfortunately the grpc lib does not offer any external
		// method to check if there are existing connections and while
		// it claims GracefulStop() will wait for outstanding RPC calls
		// to finish, some client libraries (specifically: grpc-js used
		// in nodejs/Electron apps) have trouble when the connection is
		// closed before the response to the Unlock() call is
		// completely processed. So we add a delay here to ensure
		// there's enough time before closing the grpc listener for any
		// clients to finish processing.
		time.Sleep(100 * time.Millisecond)
		grpcServer.GracefulStop()
	}()

	svc := initchainsyncrpc.New(activeChainControl.wallet)
	initchainsyncrpc.RegisterInitialChainSyncServer(grpcServer, svc)

	// wg marks when all listeners have started.
	var wg sync.WaitGroup
	for _, lis := range listeners {
		wg.Add(1)
		go func(lis *ListenerWithSignal) {
			rpcsLog.Infof("initial chain sync RPC server listening on %s",
				lis.Addr())

			// Close the ready chan to indicate we are listening.
			close(lis.Ready)

			wg.Done()
			grpcServer.Serve(lis)
		}(lis)
	}

	// Wait for gRPC server to be up running.
	wg.Wait()

	// Wait until the initial sync is done.
	select {
	case <-signal.ShutdownChannel():
		return errShutdownRequested
	case <-activeChainControl.wallet.InitialSyncChannel():
	}

	return nil
}
