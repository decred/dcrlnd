package itest

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntest/wait"
	"matheusd.com/testctx"
)

// assertCleanStateAliceBob ensures the state of the passed test nodes and the
// mempool are in a clean state (no open channels, no txs in the mempool, etc).
func assertCleanStateAliceBob(h *harnessTest, alice, bob *lntest.HarnessNode, net *lntest.NetworkHarness) {
	_, minerHeight, err := net.Miner.Node.GetBestBlock(context.TODO())
	if err != nil {
		h.Fatalf("unable to get best height: %v", err)
	}

	net.EnsureConnected(testctx.New(h.t), h.t, alice, bob)
	assertNodeBlockHeight(h, alice, int32(minerHeight))
	assertNodeBlockHeight(h, bob, int32(minerHeight))
	assertNodeNumChannels(h, alice, 0)
	assertNumPendingChannels(h, alice, 0, 0, 0, 0)
	assertNodeNumChannels(h, bob, 0)
	assertNumPendingChannels(h, bob, 0, 0, 0, 0)
	assertNumUnminedUnspent(h, alice, 0)
	assertNumUnminedUnspent(h, bob, 0)
	waitForNTxsInMempool(net.Miner.Node, 0, minerMempoolTimeout)
}

// assertCleanState ensures the state of the main test nodes and the mempool
// are in a clean state (no open channels, no txs in the mempool, etc).
func assertCleanState(h *harnessTest, net *lntest.NetworkHarness) {
	assertCleanStateAliceBob(h, net.Alice, net.Bob, net)
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

// waitForPendingHtlcs waits for up to 15 seconds for the given channel in the
// given node to show the specified number of pending HTLCs.
func waitForPendingHtlcs(node *lntest.HarnessNode,
	chanPoint *lnrpc.ChannelPoint, pendingHtlcs int) error {

	fundingTxID, err := chainhash.NewHash(chanPoint.GetFundingTxidBytes())
	if err != nil {
		return fmt.Errorf("unable to convert funding txid into "+
			"chainhash.Hash: %v", err)
	}
	outPoint := wire.OutPoint{
		Hash:  *fundingTxID,
		Index: chanPoint.OutputIndex,
	}
	targetChan := outPoint.String()

	req := &lnrpc.ListChannelsRequest{}
	ctxb := context.Background()

	var predErr error
	wait.Predicate(func() bool {
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		channelInfo, err := node.ListChannels(ctxt, req)
		if err != nil {
			predErr = err
			return false
		}

		for _, channel := range channelInfo.Channels {
			if channel.ChannelPoint != targetChan {
				continue
			}

			foundHtlcs := len(channel.PendingHtlcs)
			if foundHtlcs == pendingHtlcs {
				predErr = nil
				return true
			}

			predErr = fmt.Errorf("found only %d htlcs (wanted %d)",
				foundHtlcs, pendingHtlcs)
			return false
		}

		predErr = fmt.Errorf("could not find channel %s", targetChan)
		return false
	}, time.Second*15)
	return predErr
}

func assertNumUnminedUnspent(t *harnessTest, node *lntest.HarnessNode, expected int) {
	err := wait.NoError(func() error {
		ctxb := context.Background()
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		utxoReq := &lnrpc.ListUnspentRequest{}
		utxoResp, err := node.ListUnspent(ctxt, utxoReq)
		if err != nil {
			return fmt.Errorf("unable to query utxos: %v", err)
		}

		actual := len(utxoResp.Utxos)
		if actual != expected {
			return fmt.Errorf("node %s has wrong number of unmined utxos ("+
				"expected %d actual %d)", node.Name(), expected, actual)
		}

		return nil

	}, defaultTimeout)
	if err != nil {
		t.Fatalf("failed asserting nb of unmined unspent: %v", err)
	}
}
