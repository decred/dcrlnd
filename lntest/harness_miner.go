package lntest

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/rpcclient/v8"
	"github.com/decred/dcrlnd/internal/testutils"
	rpctest "github.com/decred/dcrtest/dcrdtest"
)

const (
	// minerLogFilename is the default log filename for the miner node.
	minerLogFilename = "output_btcd_miner.log"

	// minerLogDir is the default log dir for the miner node.
	minerLogDir = ".minerlogs"
)

var harnessNetParams = chaincfg.SimNetParams()

type HarnessMiner struct {
	*rpctest.Harness

	// Client adapts decred's dcrdtest.Harness interface to lnd's.
	Client *rpcclient.Client

	// runCtx is a context with cancel method. It's used to signal when the
	// node needs to quit, and used as the parent context when spawning
	runCtx context.Context
	cancel context.CancelFunc

	// logPath is the directory path of the miner's logs.
	logPath string

	// logFilename is the saved log filename of the miner node.
	logFilename string
}

// NewMiner creates a new miner using dcrd backend with the default log file
// dir and name.
func NewMinerTemp() (*HarnessMiner, error) { // TODO(yy): rename
	return newMiner(minerLogDir, minerLogFilename)
}

// NewTempMiner creates a new miner using dcrd backend with the specified log
// file dir and name.
func NewTempMiner(tempDir, tempLogFilename string) (*HarnessMiner, error) {
	return newMiner(tempDir, tempLogFilename)
}

// newMiner creates a new miner using btcd's rpctest.
func newMiner(minerDirName, logFilename string) (*HarnessMiner, error) {
	handler := &rpcclient.NotificationHandlers{}
	baseLogPath := fmt.Sprintf("%s/%s", GetLogDir(), minerDirName)

	args := []string{
		"--rejectnonstd",
		"--txindex",
		"--nobanning",
		"--debuglevel=debug",
		"--logdir=" + baseLogPath,
		"--maxorphantx=0",
		"--rpcmaxclients=100",
		"--rpcmaxwebsockets=100",
		"--rpcmaxconcurrentreqs=100",
		"--logsize=100M",
		"--maxsameip=200",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	miner, err := testutils.NewSetupRPCTest(ctx, 5, harnessNetParams, handler,
		args, false, 0)
	if err != nil {
		return nil, fmt.Errorf(
			"unable to create mining node: %v", err,
		)
	}

	ctxt, cancel := context.WithCancel(context.Background())
	m := &HarnessMiner{
		Harness:     miner,
		runCtx:      ctxt,
		cancel:      cancel,
		logPath:     baseLogPath,
		logFilename: logFilename,
	}
	return m, nil
}

// Stop shuts down the miner and saves its logs.
func (h *HarnessMiner) Stop() error {
	h.cancel()

	if err := h.TearDown(); err != nil {
		return fmt.Errorf("tear down miner got error: %s", err)
	}

	return h.saveLogs()
}

// saveLogs copies the node logs and save it to the file specified by
// h.logFilename.
func (h *HarnessMiner) saveLogs() error {
	// After shutting down the miner, we'll make a copy of the log files
	// before deleting the temporary log dir.
	path := fmt.Sprintf("%s/%s", h.logPath, harnessNetParams.Name)
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return fmt.Errorf("unable to read log directory: %v", err)
	}

	for _, file := range files {
		newFilename := strings.Replace(
			file.Name(), "btcd.log", h.logFilename, 1,
		)
		copyPath := fmt.Sprintf("%s/../%s", h.logPath, newFilename)

		logFile := fmt.Sprintf("%s/%s", path, file.Name())
		err := CopyFile(filepath.Clean(copyPath), logFile)
		if err != nil {
			return fmt.Errorf("unable to copy file: %v", err)
		}
	}

	if err = os.RemoveAll(h.logPath); err != nil {
		return fmt.Errorf("cannot remove dir %s: %v", h.logPath, err)
	}

	return nil
}

// waitForTxInMempool blocks until the target txid is seen in the mempool. If
// the transaction isn't seen within the network before the passed timeout,
// then an error is returned.
func (h *HarnessMiner) waitForTxInMempool(txid chainhash.Hash) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var mempool []*chainhash.Hash
	for {
		select {
		case <-h.runCtx.Done():
			return fmt.Errorf("NetworkHarness has been torn down")
		case <-time.After(DefaultTimeout):
			return fmt.Errorf("wanted %v, found %v txs "+
				"in mempool: %v", txid, len(mempool), mempool)

		case <-ticker.C:
			var err error
			mempool, err = h.Client.GetRawMempool(h.runCtx, types.GRMAll)
			if err != nil {
				return err
			}

			for _, mempoolTx := range mempool {
				if *mempoolTx == txid {
					return nil
				}
			}
		}
	}
}
