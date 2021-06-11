package itest

import (
	"context"
	"fmt"

	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntest/wait"
	"matheusd.com/testctx"
)

// assertCleanState ensures the state of the main test nodes and the mempool
// are in a clean state (no open channels, no txs in the mempool, etc).
func assertCleanState(h *harnessTest, net *lntest.NetworkHarness) {
	_, minerHeight, err := net.Miner.Node.GetBestBlock(context.TODO())
	if err != nil {
		h.Fatalf("unable to get best height: %v", err)
	}

	net.EnsureConnected(testctx.New(h.t), h.t, net.Alice, net.Bob)
	assertNodeBlockHeight(h, net.Alice, int32(minerHeight))
	assertNodeBlockHeight(h, net.Bob, int32(minerHeight))
	assertNodeNumChannels(h, net.Alice, 0)
	assertNumPendingChannels(h, net.Alice, 0, 0, 0, 0)
	assertNodeNumChannels(h, net.Bob, 0)
	assertNumPendingChannels(h, net.Bob, 0, 0, 0, 0)
	assertNumUnminedUnspent(h, net.Alice, 0)
	assertNumUnminedUnspent(h, net.Bob, 0)
	waitForNTxsInMempool(net.Miner.Node, 0, minerMempoolTimeout)
}

func assertNodeBlockHeight(t *harnessTest, node *lntest.HarnessNode, height int32) {
	t.t.Helper()

	err := wait.NoError(func() error {
		ctxt, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		defer cancel()
		getInfoReq := &lnrpc.GetInfoRequest{}
		getInfoResp, err := node.GetInfo(ctxt, getInfoReq)
		if err != nil {
			return err
		}
		if int32(getInfoResp.BlockHeight) != height {
			return fmt.Errorf("unexpected block height for node %s: "+
				"want=%d got=%d", node.Name(),
				height, getInfoResp.BlockHeight)
		}

		return nil
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("failed to assert node block height: %v", err)
	}
}

// recordedTxFee returns the tx fee recorded in the transaction itself (that
// is, sum(TxOut[].Value) - sum(TxIn[].ValueIn)). While this is not currently
// enforced by consensus rules and cannot be relied upon for validation
// purposes, it's sufficient for testing purposes, assuming the procedure that
// generated the transaction correctly fills the ValueIn (which should be true
// for transactions produced by dcrlnd).
func recordedTxFee(tx *wire.MsgTx) int64 {
	var amountIn, amountOut int64
	for _, in := range tx.TxIn {
		amountIn += in.ValueIn
	}
	for _, out := range tx.TxOut {
		amountOut += out.Value
	}
	return amountIn - amountOut
}
