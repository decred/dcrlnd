//go:build rpctest
// +build rpctest

package itest

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"github.com/decred/dcrd/dcrutil/v4"
	jsonrpctypes "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/internal/psbt"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
)

// testPsbtChanFunding makes sure a channel can be opened between carol and dave
// by using a Partially Signed Bitcoin Transaction that funds the channel
// multisig funding output.
func testPsbtChanFunding(net *lntest.NetworkHarness, t *harnessTest) {
	t.Skipf("psbt is not currently implemented in dcrlnd")

	ctxb := context.Background()
	const chanSize = defaultChanAmt

	// First, we'll create two new nodes that we'll use to open channel
	// between for this test.
	carol, err := net.NewNode("carol", nil)
	require.NoError(t.t, err)
	defer shutdownAndAssert(net, t, carol)

	dave, err := net.NewNode("dave", nil)
	require.NoError(t.t, err)
	defer shutdownAndAssert(net, t, dave)

	// Before we start the test, we'll ensure both sides are connected so
	// the funding flow can be properly executed.
	ctxt, cancel := context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	err = net.EnsureConnected(ctxt, carol, dave)
	require.NoError(t.t, err)
	err = net.EnsureConnected(ctxt, carol, net.Alice)
	require.NoError(t.t, err)

	// At this point, we can begin our PSBT channel funding workflow. We'll
	// start by generating a pending channel ID externally that will be used
	// to track this new funding type.
	var pendingChanID [32]byte
	_, err = rand.Read(pendingChanID[:])
	require.NoError(t.t, err)

	// We'll also test batch funding of two channels so we need another ID.
	var pendingChanID2 [32]byte
	_, err = rand.Read(pendingChanID2[:])
	require.NoError(t.t, err)

	// Now that we have the pending channel ID, Carol will open the channel
	// by specifying a PSBT shim. We use the NoPublish flag here to avoid
	// publishing the whole batch TX too early.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	chanUpdates, psbtBytes, err := openChannelPsbt(
		ctxt, carol, dave, lntest.OpenChannelParams{
			Amt: chanSize,
			FundingShim: &lnrpc.FundingShim{
				Shim: &lnrpc.FundingShim_PsbtShim{
					PsbtShim: &lnrpc.PsbtShim{
						PendingChanId: pendingChanID[:],
						NoPublish:     true,
					},
				},
			},
		},
	)
	require.NoError(t.t, err)
	packet, err := psbt.NewFromRawBytes(bytes.NewReader(psbtBytes), false)
	require.NoError(t.t, err)

	// Let's add a second channel to the batch. This time between carol and
	// alice. We will the batch TX once this channel funding is complete.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	chanUpdates2, psbtBytes2, err := openChannelPsbt(
		ctxt, carol, net.Alice, lntest.OpenChannelParams{
			Amt: chanSize,
			FundingShim: &lnrpc.FundingShim{
				Shim: &lnrpc.FundingShim_PsbtShim{
					PsbtShim: &lnrpc.PsbtShim{
						PendingChanId: pendingChanID2[:],
						NoPublish:     false,
					},
				},
			},
		},
	)
	require.NoError(t.t, err)
	packet2, err := psbt.NewFromRawBytes(bytes.NewReader(psbtBytes2), false)
	require.NoError(t.t, err)

	// We'll now create a fully signed transaction that sends to the outputs
	// encoded in the PSBT. We'll let the miner do it and convert the final
	// TX into a PSBT, that's way easier than assembling a PSBT manually.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	allOuts := append(packet.UnsignedTx.TxOut, packet2.UnsignedTx.TxOut...)
	finalTx, err := net.Miner.CreateTransaction(ctxt, allOuts, 5)
	require.NoError(t.t, err)

	// The helper function splits the final TX into the non-witness data
	// encoded in a PSBT and the witness data returned separately.
	unsignedPsbt, scripts, witnesses, err := createPsbtFromSignedTx(finalTx)
	require.NoError(t.t, err)

	// The PSBT will also be checked if there are large enough inputs
	// present. We need to add some fake UTXO information to the PSBT to
	// tell it what size of inputs we have.
	for idx, txIn := range unsignedPsbt.UnsignedTx.TxIn {
		utxPrevOut := txIn.PreviousOutPoint.Index
		fakeUtxo := &wire.MsgTx{
			Version: 2,
			TxIn:    []*wire.TxIn{{}},
			TxOut:   make([]*wire.TxOut, utxPrevOut+1),
		}
		for idx := range fakeUtxo.TxOut {
			fakeUtxo.TxOut[idx] = &wire.TxOut{}
		}
		fakeUtxo.TxOut[utxPrevOut].Value = 10000000000
		unsignedPsbt.Inputs[idx].NonWitnessUtxo = fakeUtxo
	}

	// Serialize the PSBT with the faked UTXO information.
	var buf bytes.Buffer
	err = unsignedPsbt.Serialize(&buf)
	require.NoError(t.t, err)

	// We have a PSBT that has no witness data yet, which is exactly what we
	// need for the next step: Verify the PSBT with the funding intents.
	_, err = carol.FundingStateStep(ctxb, &lnrpc.FundingTransitionMsg{
		Trigger: &lnrpc.FundingTransitionMsg_PsbtVerify{
			PsbtVerify: &lnrpc.FundingPsbtVerify{
				PendingChanId: pendingChanID[:],
				FundedPsbt:    buf.Bytes(),
			},
		},
	})
	require.NoError(t.t, err)
	_, err = carol.FundingStateStep(ctxb, &lnrpc.FundingTransitionMsg{
		Trigger: &lnrpc.FundingTransitionMsg_PsbtVerify{
			PsbtVerify: &lnrpc.FundingPsbtVerify{
				PendingChanId: pendingChanID2[:],
				FundedPsbt:    buf.Bytes(),
			},
		},
	})
	require.NoError(t.t, err)

	// Now we'll add the witness data back into the PSBT to make it a
	// complete and signed transaction that can be finalized. We'll trick
	// a bit by putting the script sig back directly, because we know we
	// will only get non-witness outputs from the miner wallet.
	for idx := range finalTx.TxIn {
		require.Greater(t.t, len(witnesses[idx]), 0)
		unsignedPsbt.Inputs[idx].FinalScriptSig = scripts[idx]
	}

	// We've signed our PSBT now, let's pass it to the intent again.
	buf.Reset()
	err = unsignedPsbt.Serialize(&buf)
	require.NoError(t.t, err)
	_, err = carol.FundingStateStep(ctxb, &lnrpc.FundingTransitionMsg{
		Trigger: &lnrpc.FundingTransitionMsg_PsbtFinalize{
			PsbtFinalize: &lnrpc.FundingPsbtFinalize{
				PendingChanId: pendingChanID[:],
				SignedPsbt:    buf.Bytes(),
			},
		},
	})
	require.NoError(t.t, err)

	// Consume the "channel pending" update. This waits until the funding
	// transaction was fully compiled.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	updateResp, err := receiveChanUpdate(ctxt, chanUpdates)
	require.NoError(t.t, err)
	upd, ok := updateResp.Update.(*lnrpc.OpenStatusUpdate_ChanPending)
	require.True(t.t, ok)
	chanPoint := &lnrpc.ChannelPoint{
		FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
			FundingTxidBytes: upd.ChanPending.Txid,
		},
		OutputIndex: upd.ChanPending.OutputIndex,
	}

	// No transaction should have been published yet.
	mempool, err := net.Miner.Node.GetRawMempool(context.Background(), jsonrpctypes.GRMRegular)
	require.NoError(t.t, err)
	require.Equal(t.t, 0, len(mempool))

	// Let's progress the second channel now. This time we'll use the raw
	// wire format transaction directly.
	buf.Reset()
	err = finalTx.Serialize(&buf)
	require.NoError(t.t, err)
	_, err = carol.FundingStateStep(ctxb, &lnrpc.FundingTransitionMsg{
		Trigger: &lnrpc.FundingTransitionMsg_PsbtFinalize{
			PsbtFinalize: &lnrpc.FundingPsbtFinalize{
				PendingChanId: pendingChanID2[:],
				FinalRawTx:    buf.Bytes(),
			},
		},
	})
	require.NoError(t.t, err)

	// Consume the "channel pending" update for the second channel. This
	// waits until the funding transaction was fully compiled and in this
	// case published.
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	updateResp2, err := receiveChanUpdate(ctxt, chanUpdates2)
	require.NoError(t.t, err)
	upd2, ok := updateResp2.Update.(*lnrpc.OpenStatusUpdate_ChanPending)
	require.True(t.t, ok)
	chanPoint2 := &lnrpc.ChannelPoint{
		FundingTxid: &lnrpc.ChannelPoint_FundingTxidBytes{
			FundingTxidBytes: upd2.ChanPending.Txid,
		},
		OutputIndex: upd2.ChanPending.OutputIndex,
	}

	// Great, now we can mine a block to get the transaction confirmed, then
	// wait for the new channel to be propagated through the network.
	txHash := finalTx.TxHash()
	block := mineBlocks(t, net, 6, 1)[0]
	assertTxInBlock(t, block, &txHash)
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	err = carol.WaitForNetworkChannelOpen(ctxt, chanPoint)
	require.NoError(t.t, err)
	err = carol.WaitForNetworkChannelOpen(ctxt, chanPoint2)
	require.NoError(t.t, err)

	// With the channel open, ensure that it is counted towards Carol's
	// total channel balance.
	balReq := &lnrpc.ChannelBalanceRequest{}
	ctxt, cancel = context.WithTimeout(ctxb, defaultTimeout)
	defer cancel()
	balRes, err := carol.ChannelBalance(ctxt, balReq)
	require.NoError(t.t, err)
	require.NotEqual(t.t, int64(0), balRes.LocalBalance.Atoms)

	// Next, to make sure the channel functions as normal, we'll make some
	// payments within the channel.
	payAmt := dcrutil.Amount(100000)
	invoice := &lnrpc.Invoice{
		Memo:  "new chans",
		Value: int64(payAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp, err := dave.AddInvoice(ctxt, invoice)
	require.NoError(t.t, err)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, carol, carol.RouterClient, []string{resp.PaymentRequest},
		true,
	)
	require.NoError(t.t, err)

	// To conclude, we'll close the newly created channel between Carol and
	// Dave. This function will also block until the channel is closed and
	// will additionally assert the relevant channel closing post
	// conditions.
	ctxt, cancel = context.WithTimeout(ctxb, channelCloseTimeout)
	defer cancel()
	closeChannelAndAssert(ctxt, t, net, carol, chanPoint, false)
}

// openChannelPsbt attempts to open a channel between srcNode and destNode with
// the passed channel funding parameters. If the passed context has a timeout,
// then if the timeout is reached before the channel pending notification is
// received, an error is returned. An error is returned if the expected step
// of funding the PSBT is not received from the source node.
func openChannelPsbt(ctx context.Context, srcNode, destNode *lntest.HarnessNode,
	p lntest.OpenChannelParams) (lnrpc.Lightning_OpenChannelClient, []byte,
	error) {

	// Wait until srcNode and destNode have the latest chain synced.
	// Otherwise, we may run into a check within the funding manager that
	// prevents any funding workflows from being kicked off if the chain
	// isn't yet synced.
	if err := srcNode.WaitForBlockchainSync(ctx); err != nil {
		return nil, nil, fmt.Errorf("unable to sync srcNode chain: %v",
			err)
	}
	if err := destNode.WaitForBlockchainSync(ctx); err != nil {
		return nil, nil, fmt.Errorf("unable to sync destNode chain: %v",
			err)
	}

	// Send the request to open a channel to the source node now. This will
	// open a long-lived stream where we'll receive status updates about the
	// progress of the channel.
	respStream, err := srcNode.OpenChannel(ctx, &lnrpc.OpenChannelRequest{
		NodePubkey:         destNode.PubKey[:],
		LocalFundingAmount: int64(p.Amt),
		PushAtoms:          int64(p.PushAmt),
		Private:            p.Private,
		SpendUnconfirmed:   p.SpendUnconfirmed,
		MinHtlcMAtoms:      int64(p.MinHtlc),
		FundingShim:        p.FundingShim,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("unable to open channel between "+
			"source and dest: %v", err)
	}

	// Consume the "PSBT funding ready" update. This waits until the node
	// notifies us that the PSBT can now be funded.
	resp, err := receiveChanUpdate(ctx, respStream)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to consume channel update "+
			"message: %v", err)
	}
	upd, ok := resp.Update.(*lnrpc.OpenStatusUpdate_PsbtFund)
	if !ok {
		return nil, nil, fmt.Errorf("expected PSBT funding update, "+
			"instead got %v", resp)
	}
	return respStream, upd.PsbtFund.Psbt, nil
}

// receiveChanUpdate waits until a message is received on the stream or the
// context is canceled. The context must have a timeout or must be canceled
// in case no message is received, otherwise this function will block forever.
func receiveChanUpdate(ctx context.Context,
	stream lnrpc.Lightning_OpenChannelClient) (*lnrpc.OpenStatusUpdate,
	error) {

	chanMsg := make(chan *lnrpc.OpenStatusUpdate)
	errChan := make(chan error)
	go func() {
		// Consume one message. This will block until the message is
		// recieved.
		resp, err := stream.Recv()
		if err != nil {
			errChan <- err
			return
		}
		chanMsg <- resp
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout reached before chan pending " +
			"update sent")

	case err := <-errChan:
		return nil, err

	case updateMsg := <-chanMsg:
		return updateMsg, nil
	}
}

type txWitness [][]byte

// createPsbtFromSignedTx is a utility function to create a PSBT from an
// already-signed transaction, so we can test reconstructing, signing and
// extracting it. Returned are: an unsigned transaction serialization, a list
// of scriptSigs, one per input, and a list of witnesses, one per input.
func createPsbtFromSignedTx(tx *wire.MsgTx) (*psbt.Packet, [][]byte,
	[]txWitness, error) {

	scriptSigs := make([][]byte, 0, len(tx.TxIn))
	witnesses := make([]txWitness, 0, len(tx.TxIn))
	tx2 := tx.Copy()

	// Blank out signature info in inputs
	for i, tin := range tx2.TxIn {
		tin.SignatureScript = nil
		scriptSigs = append(scriptSigs, tx.TxIn[i].SignatureScript)
		// tin.Witness = nil
		// witnesses = append(witnesses, tx.TxIn[i].Witness)
	}

	// Outputs always contain: (value, scriptPubkey) so don't need
	// amending.  Now tx2 is tx with all signing data stripped out
	unsignedPsbt, err := psbt.NewFromUnsignedTx(tx2)
	if err != nil {
		return nil, nil, nil, err
	}
	return unsignedPsbt, scriptSigs, witnesses, nil
}
