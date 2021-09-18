package lntest

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	rpctest "github.com/decred/dcrtest/dcrdtest"
)

// setupVotingWallet sets up a minimum voting wallet, so that the simnet used
// for tests can advance past SVH.
func (h *HarnessMiner) setupVotingWallet() error {
	vwCtx, vwCancel := context.WithCancel(h.runCtx)
	vw, err := rpctest.NewVotingWallet(vwCtx, h.Harness)
	if err != nil {
		vwCancel()
		return err
	}

	// Use a custom miner on the voting wallet that ensures simnet blocks
	// are generated as fast as possible without triggering PoW difficulty
	// increases.
	vw.SetMiner(func(ctx context.Context, nb uint32) ([]*chainhash.Hash, error) {
		return rpctest.AdjustedSimnetMiner(ctx, h.Node, nb)
	})

	err = vw.Start(vwCtx)
	if err != nil {
		defer vwCancel()
		return err
	}

	h.votingWallet = vw
	h.votingWalletCancel = vwCancel
	return nil
}

// SetUpChain performs the initial chain setup for integration tests. This
// should be done only once.
func (h *HarnessMiner) SetUpChain() error {
	// Generate the premine block the usual way.
	ctx, cancel := context.WithTimeout(h.runCtx, 30*time.Second)
	defer cancel()
	_, err := h.Node.Generate(ctx, 1)
	if err != nil {
		return fmt.Errorf("unable to generate premine: %v", err)
	}

	// Generate enough blocks so that the network harness can have funds to
	// send to the voting wallet, Alice and Bob.
	_, err = rpctest.AdjustedSimnetMiner(ctx, h.Node, 64)
	if err != nil {
		return fmt.Errorf("unable to init chain: %v", err)
	}

	// Setup a ticket buying/voting dcrwallet, so that the network advances
	// past SVH.
	err = h.setupVotingWallet()
	if err != nil {
		return err
	}

	return nil
}

// ModifyTestCaseName modifies the current test case name.
func (n *NetworkHarness) ModifyTestCaseName(testCase string) {
	n.currentTestCase = testCase
}

func (hn *HarnessNode) LogPrintf(format string, args ...interface{}) error {
	now := time.Now().Format("2006-01-02 15:04:05.999")
	f := now + " ----------: " + format
	hn.AddToLog(f, args...)
	return nil
}
