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

// logDirPattern is the pattern of the temporary log directory.
const logDirPattern = "%s/.backendlogs"

// newBackend initializes a new dcrd rpctest backend.
//
//nolint:unused
func newBackend(t *testing.T, miner *rpctest.Harness) (*rpctest.Harness, func() error, error) {
	baseLogDir := fmt.Sprintf(logDirPattern, GetLogDir())
	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--debuglevel=debug",
		"--logdir=" + baseLogDir,
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
		testctx.New(t), 5, netParams, nil, args, false, 0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to create dcrd node: %v", err)
	}

	cleanUp := func() error {
		var errStr string
		if err := chainBackend.TearDown(); err != nil {
			errStr += err.Error() + "\n"
		}

		// After shutting down the chain backend, we'll make a copy of
		// the log file before deleting the temporary log dir.
		logFile := baseLogDir + "/" + netParams.Name + "/dcrd.log"
		destLogFile := fmt.Sprintf("%s/output_dcrd_chainbackend.log", GetLogDir())
		err := CopyFile(destLogFile, logFile)
		if err != nil {
			errStr += fmt.Sprintf("unable to copy file: %v\n", err)
		}
		if err = os.RemoveAll(baseLogDir); err != nil {
			errStr += fmt.Sprintf("Cannot remove dir %s: %v\n", baseLogDir, err)
		}
		if errStr != "" {
			return errors.New(errStr)
		}
		return nil
	}

	return chainBackend, cleanUp, nil
}
