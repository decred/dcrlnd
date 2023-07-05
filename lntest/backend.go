package lntest

import (
	"fmt"
	"os"
	"testing"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrlnd/internal/testutils"
	rpctest "github.com/decred/dcrtest/dcrdtest"
	"matheusd.com/testctx"
)

func newBackend(t *testing.T, miner *rpctest.Harness, logDir string) (*rpctest.Harness, func(), error) {
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

	// Connect this newly created node to the miner.
	rpctest.ConnectNode(testctx.New(t), chainBackend, miner)

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

	return chainBackend, cleanUp, nil
}
