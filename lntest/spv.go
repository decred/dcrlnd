//go:build spv
// +build spv

package lntest

import (
	"context"
	"fmt"
	"os"
	"testing"

	pb "decred.org/dcrwallet/v3/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpctest"
	"github.com/decred/dcrlnd/internal/testutils"
)

// logDir is the name of the temporary log directory.
const logDir = "./.backendlogs"

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

func unsafeFindP2PAddr(miner, chainBackend *rpctest.Harness) (string, error) {
	// This assumes the miner doesn't have any connections yet.
	err := rpctest.ConnectNode(context.Background(), miner, chainBackend)
	if err != nil {
		return "", err
	}

	peers, err := miner.Node.GetPeerInfo(context.Background())
	if err != nil {
		return "", err
	}

	err = rpctest.RemoveNode(context.Background(), miner, chainBackend)
	if err != nil {
		return "", err
	}

	return peers[0].Addr, nil
}

// NewBackend starts a new rpctest.Harness and returns a SpvBackendConfig for
// that node.
func NewBackend(t *testing.T, miner *rpctest.Harness) (*SpvBackendConfig, func(), error) {
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

	// FIXME: This is really brittle and needs to be fixed with a specific
	// call in the upstream dcrd/rpctest package.
	//
	// Determine the p2p address by having the miner connect to this node
	// and figuring out the address from the miner.
	connectAddr, err := unsafeFindP2PAddr(miner, chainBackend)
	if err != nil {
		return nil, nil, err
	}

	bd := &SpvBackendConfig{
		connectAddr: connectAddr,
		harness:     chainBackend,
		miner:       miner,
	}

	// Connect this newly created node to the miner.
	rpctest.ConnectNode(context.Background(), chainBackend, miner)

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
