package lntest

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrlnd/internal/testutils"
	rpctest "github.com/decred/dcrtest/dcrdtest"
	"matheusd.com/testctx"
)

func newBackend(t *testing.T, miner *rpctest.Harness, logDir string) (*rpctest.Harness, func() error, error) {
	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--debuglevel=debug",
		"--logdir=" + logDir,
		"--maxorphantx=0",
		"--nobanning",
		"--rpcmaxclients=100",
		"--rpcmaxwebsockets=100",
		"--rpcmaxconcurrentreqs=100",
		"--logsize=100M",
		"--maxsameip=200",
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

	cleanUp := func() error {
		var errStr string
		if err := chainBackend.TearDown(); err != nil {
			errStr += err.Error() + "\n"
		}

		// After shutting down the chain backend, we'll make a copy of
		// the log file before deleting the temporary log dir.
		logFile := logDir + "/" + netParams.Name + "/dcrd.log"
		err := CopyFile("./output_dcrd_chainbackend.log", logFile)
		if err != nil {
			errStr += fmt.Sprintf("unable to copy file: %v\n", err)
		}
		if err = os.RemoveAll(logDir); err != nil {
			errStr += fmt.Sprintf("Cannot remove dir %s: %v\n", logDir, err)
		}
		if errStr != "" {
			return errors.New(errStr)
		}
		return nil
	}

	return chainBackend, cleanUp, nil
}
