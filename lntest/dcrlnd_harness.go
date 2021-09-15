package lntest

import (
	"context"
	"fmt"

	rpctest "github.com/decred/dcrtest/dcrdtest"
)

// SetUpChain performs the initial chain setup for integration tests. This
// should be done only once.
func (n *NetworkHarness) SetUpChain() error {
	// Generate the premine block the usual way.
	_, err := n.Miner.Node.Generate(context.TODO(), 1)
	if err != nil {
		return fmt.Errorf("unable to generate premine: %v", err)
	}

	// Generate enough blocks so that the network harness can have funds to
	// send to the voting wallet, Alice and Bob.
	_, err = rpctest.AdjustedSimnetMiner(context.Background(), n.Miner.Node, 64)
	if err != nil {
		return fmt.Errorf("unable to init chain: %v", err)
	}

	// Setup a ticket buying/voting dcrwallet, so that the network advances
	// past SVH.
	err = n.setupVotingWallet()
	if err != nil {
		return err
	}

	return nil
}

// ModifyTestCaseName modifies the current test case name.
func (n *NetworkHarness) ModifyTestCaseName(testCase string) {
	n.currentTestCase = testCase
}
