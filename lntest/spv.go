//go:build spv
// +build spv

package lntest

import (
	"context"
	"testing"

	pb "decred.org/dcrwallet/v5/rpc/walletrpc"
	rpctest "github.com/decred/dcrtest/dcrdtest"
)

// SpvBackendConfig is an implementation of the BackendConfig interface
// backed by a btcd node.
type SpvBackendConfig struct {
	// connectAddr is the address that SPV clients may use to connect to
	// this node via the p2p interface.
	connectAddr string

	// harness is this backend's node.
	harness *rpctest.Harness

	// miner is the backing miner used during tests.
	miner *rpctest.Harness
}

// GenArgs returns the arguments needed to be passed to LND at startup for
// using this node as a chain backend.
func (b SpvBackendConfig) GenArgs() []string {
	return []string{
		"--dcrwallet.spv",
		"--dcrwallet.spvconnect=" + b.harness.P2PAddress(),
	}
}

func (b SpvBackendConfig) StartWalletSync(loader pb.WalletLoaderServiceClient, password []byte) error {
	req := &pb.SpvSyncRequest{
		SpvConnect:        []string{b.connectAddr},
		DiscoverAccounts:  true,
		PrivatePassphrase: password,
	}

	stream, err := loader.SpvSync(context.Background(), req)
	if err != nil {
		return err
	}

	syncDone := make(chan error)
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				syncDone <- err
				return
			}
			if resp.Synced {
				close(syncDone)
				break
			}
		}

		// After sync is complete, just drain the notifications until
		// the connection is closed.
		for {
			_, err := stream.Recv()
			if err != nil {
				return
			}
		}
	}()

	return <-syncDone
}

// ConnectMiner connects the backend to the underlying miner.
func (b SpvBackendConfig) ConnectMiner() error {
	return rpctest.ConnectNode(context.Background(), b.harness, b.miner)
}

// DisconnectMiner disconnects the backend to the underlying miner.
func (b SpvBackendConfig) DisconnectMiner() error {
	return rpctest.RemoveNode(context.Background(), b.harness, b.miner)
}

// Name returns the name of the backend type.
func (b SpvBackendConfig) Name() string {
	return "spv"
}

// NewBackend starts a new rpctest.Harness and returns a SpvBackendConfig for
// that node.
func NewBackend(t *testing.T, miner *rpctest.Harness) (*SpvBackendConfig, func() error, error) {
	chainBackend, cleanUp, err := newBackend(t, miner)
	if err != nil {
		return nil, nil, err
	}

	bd := &SpvBackendConfig{
		connectAddr: chainBackend.P2PAddress(),
		harness:     chainBackend,
		miner:       miner,
	}
	return bd, cleanUp, nil
}
