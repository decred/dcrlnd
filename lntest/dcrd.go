//go:build dcrd
// +build dcrd

package lntest

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	pb "decred.org/dcrwallet/v2/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpcclient/v7"
	"github.com/decred/dcrd/rpctest"
	"github.com/decred/dcrlnd/internal/testutils"
)

// logDir is the name of the temporary log directory.
const logDir = "./.backendlogs"

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
	return rpctest.ConnectNode(b.harness, b.miner)
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
func NewBackend(t *testing.T, miner *rpctest.Harness) (*DcrdBackendConfig, func(), error) {
	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--debuglevel=debug",
		"--logdir=" + logDir,
		"--maxorphantx=0",
		"--nobanning",
	}
	netParams := chaincfg.SimNetParams()
	chainBackend, err := testutils.NewSetupRPCTest(
		t, 5, netParams, nil, args, false, 0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create dcrd node: %v", err)
	}

	bd := &DcrdBackendConfig{
		rpcConfig: chainBackend.RPCConfig(),
		harness:   chainBackend,
		miner:     miner,
	}

	// Connect this newly created node to the miner.
	rpctest.ConnectNode(chainBackend, miner)

	cleanUp := func() {
		chainBackend.TearDown()

		// After shutting down the chain backend, we'll make a copy of
		// the log file before deleting the temporary log dir.
		logFile := logDir + "/" + netParams.Name + "/dcrd.log"
		err := CopyFile("./output_dcrd_chainbackend.log", logFile)
		if err != nil {
			fmt.Printf("unable to copy file: %v\n", err)
		}
		if err = os.RemoveAll(logDir); err != nil {
			fmt.Printf("Cannot remove dir %s: %v\n", logDir, err)
		}
	}

	return bd, cleanUp, nil
}
