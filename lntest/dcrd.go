//go:build !spv
// +build !spv

package lntest

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	pb "decred.org/dcrwallet/v4/rpc/walletrpc"
	"github.com/decred/dcrd/rpcclient/v8"
	rpctest "github.com/decred/dcrtest/dcrdtest"
)

// DcrdBackendConfig is an implementation of the BackendConfig interface
// backed by a btcd node.
type DcrdBackendConfig struct {
	// rpcConfig  houses the connection config to the backing dcrd
	// instance.
	rpcConfig rpcclient.ConnConfig

	// harness is this backend's node.
	harness *rpctest.Harness

	// miner is the backing miner used during tests.
	miner *rpctest.Harness
}

// GenArgs returns the arguments needed to be passed to LND at startup for
// using this node as a chain backend.
func (b DcrdBackendConfig) GenArgs() []string {
	var args []string
	encodedCert := hex.EncodeToString(b.rpcConfig.Certificates)
	args = append(args, fmt.Sprintf("--dcrd.rpchost=%v", b.rpcConfig.Host))
	args = append(args, fmt.Sprintf("--dcrd.rpcuser=%v", b.rpcConfig.User))
	args = append(args, fmt.Sprintf("--dcrd.rpcpass=%v", b.rpcConfig.Pass))
	args = append(args, fmt.Sprintf("--dcrd.rawrpccert=%v", encodedCert))

	return args
}

func (b DcrdBackendConfig) StartWalletSync(loader pb.WalletLoaderServiceClient, password []byte) error {
	req := &pb.RpcSyncRequest{
		NetworkAddress:    b.rpcConfig.Host,
		Username:          b.rpcConfig.User,
		Password:          []byte(b.rpcConfig.Pass),
		Certificate:       b.rpcConfig.Certificates,
		DiscoverAccounts:  true,
		PrivatePassphrase: password,
	}

	stream, err := loader.RpcSync(context.Background(), req)
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
func (b DcrdBackendConfig) ConnectMiner() error {
	return rpctest.ConnectNode(context.Background(), b.harness, b.miner)
}

// DisconnectMiner disconnects the backend to the underlying miner.
func (b DcrdBackendConfig) DisconnectMiner() error {
	return rpctest.RemoveNode(context.Background(), b.harness, b.miner)
}

// Name returns the name of the backend type.
func (b DcrdBackendConfig) Name() string {
	return "dcrd"
}

// NewBackend starts a new rpctest.Harness and returns a DcrdBackendConfig for
// that node.
func NewBackend(t *testing.T, miner *rpctest.Harness) (*DcrdBackendConfig, func() error, error) {
	chainBackend, cleanUp, err := newBackend(t, miner)
	if err != nil {
		return nil, nil, err
	}

	bd := &DcrdBackendConfig{
		rpcConfig: chainBackend.RPCConfig(),
		harness:   chainBackend,
		miner:     miner,
	}

	return bd, cleanUp, nil
}
