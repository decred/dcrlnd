package itest

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	jsonrpctypes "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntest/wait"
	"github.com/decred/dcrlnd/routing"
	"github.com/stretchr/testify/require"
)

// testRevokedCloseRetributionRemoteHodlSecondLevel tests that Dave properly
// responds to a channel breach made by the remote party, specifically in the
// case that the remote party breaches before settling extended HTLCs.
//
// In this test we specifically wait for Carol to redeem her HTLCs via a second
// level transaction before initiating Dave's retribution detection and justice
// service.
//
// This is a dcrlnd-only test, split off from the original
// testRevokedCloseRetributionRemoteHodl.
func testRevokedCloseRetributionRemoteHodlSecondLevel(net *lntest.NetworkHarness,
	t *harnessTest) {
	ctxb := context.Background()

	// Currently disabled in SPV due to
	// https://github.com/decred/dcrlnd/issues/96. Re-assess after that is
	// fixed.
	if net.BackendCfg.Name() == "spv" {
		t.Skipf("Skipping for SPV for the moment")
	}

	const (
		initialBalance = int64(dcrutil.AtomsPerCoin)
		chanAmt        = defaultChanAmt
		pushAmt        = 400000
		paymentAmt     = 20000
		numInvoices    = 6
	)

	// In order to make the test non-flaky and easier to reason about,
	// we'll redefine the CSV and CLTV limits for Carol and Dave. We'll
	// define these limits such that, after force-closing the channel (with
	// carol's older, revoked state) we can mine a few blocks before she'll
	// attempt to sweep the HTLCs.

	var (
		// The new CLTV delta will be the minimum the node will accept
		// + 4 so that it doesn't immediately close the channel after
		// restarting.
		cltvDelta = routing.MinCLTVDelta + 4

		// The new CSV delay will be the previous delta + 2 so that
		// Carol doesn't attempt to redeem the CSV-encumbered
		// commitment output before Dave can send the justice tx.
		csvDelay = cltvDelta + 2 + int(routing.BlockPadding)
	)

	// Since this test will result in the counterparty being left in a
	// weird state, we will introduce another node into our test network:
	// Carol.
	carol := net.NewNode(t.t, "Carol", []string{
		"--hodl.exit-settle",
		fmt.Sprintf("--timelockdelta=%d", cltvDelta),
		fmt.Sprintf("--defaultremotedelay=%d", csvDelay),
	})
	defer shutdownAndAssert(net, t, carol)

	// We'll also create a new node Dave, who will have a channel with
	// Carol, and also use similar settings so we can broadcast a commit
	// with active HTLCs. Dave will be the breached party. We set
	// --nolisten to ensure Carol won't be able to connect to him and
	// trigger the channel data protection logic automatically.
	dave := net.NewNode(t.t, "Dave", []string{
		"--hodl.exit-settle",
		"--nolisten",
		fmt.Sprintf("--timelockdelta=%d", cltvDelta),
		fmt.Sprintf("--defaultremotedelay=%d", csvDelay),
	})
	defer shutdownAndAssert(net, t, dave)

	// We must let Dave communicate with Carol before they are able to open
	// channel, so we connect Dave and Carol,
	net.ConnectNodes(t.t, dave, carol)

	// Before we make a channel, we'll load up Dave with some coins sent
	// directly from the miner.
	net.SendCoins(t.t, dcrutil.Amount(initialBalance), dave)

	// In order to test Dave's response to an uncooperative channel closure
	// by Carol, we'll first open up a channel between them with a
	// defaultChanAmt (2^24) atoms value.
	chanPoint := openChannelAndAssert(
		t, net, dave, carol,
		lntest.OpenChannelParams{
			Amt:     chanAmt,
			PushAmt: pushAmt,
		},
	)

	// Store the channel type for later.
	cType, err := channelCommitType(carol, chanPoint)
	if err != nil {
		t.Fatalf("unable to get channel type: %v", err)
	}

	// With the channel open, we'll create a few invoices for Carol that
	// Dave will pay to in order to advance the state of the channel.
	carolPayReqs, _, _, err := createPayReqs(
		carol, paymentAmt, numInvoices,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// We'll introduce a closure to validate that Carol's current balance
	// matches the given expected amount.
	checkCarolBalance := func(expectedAmt int64) {
		carolChan, err := getChanInfo(carol)
		if err != nil {
			t.Fatalf("unable to get carol's channel info: %v", err)
		}
		if carolChan.LocalBalance != expectedAmt {
			t.Fatalf("carol's balance is incorrect, "+
				"got %v, expected %v", carolChan.LocalBalance,
				expectedAmt)
		}
	}

	// We'll introduce another closure to validate that Carol's current
	// number of updates is at least as large as the provided minimum
	// number.
	checkCarolNumUpdatesAtLeast := func(minimum uint64) {
		carolChan, err := getChanInfo(carol)
		if err != nil {
			t.Fatalf("unable to get carol's channel info: %v", err)
		}
		if carolChan.NumUpdates < minimum {
			t.Fatalf("carol's numupdates is incorrect, want %v "+
				"to be at least %v", carolChan.NumUpdates,
				minimum)
		}
	}

	// Wait for Dave to receive the channel edge from the funding manager.
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	err = dave.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("dave didn't see the dave->carol channel before "+
			"timeout: %v", err)
	}

	// Ensure that carol's balance starts with the amount we pushed to her.
	checkCarolBalance(pushAmt)

	// Send payments from Dave to Carol using 3 of Carol's payment hashes
	// generated above.
	err = completePaymentRequests(
		dave, dave.RouterClient, carolPayReqs[:numInvoices/2], false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// At this point, we'll also send over a set of HTLC's from Carol to
	// Dave. This ensures that the final revoked transaction has HTLC's in
	// both directions.
	davePayReqs, _, _, err := createPayReqs(
		dave, paymentAmt, numInvoices,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// Send payments from Carol to Dave using 3 of Dave's payment hashes
	// generated above.
	err = completePaymentRequests(
		carol, carol.RouterClient, davePayReqs[:numInvoices/2], false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// Next query for Carol's channel state, as we sent 3 payments of 10k
	// atoms each, however Carol should now see her balance as being
	// equal to the push amount in atoms since she has not settled.
	carolChan, err := getChanInfo(carol)
	if err != nil {
		t.Fatalf("unable to get carol's channel info: %v", err)
	}

	// Grab Carol's current commitment height (update number), we'll later
	// revert her to this state after additional updates to force her to
	// broadcast this soon to be revoked state.
	carolStateNumPreCopy := carolChan.NumUpdates

	// Ensure that carol's balance still reflects the original amount we
	// pushed to her, minus the HTLCs she just sent to Dave.
	checkCarolBalance(pushAmt - 3*paymentAmt)

	// Since Carol has not settled, she should only see at least one update
	// to her channel.
	checkCarolNumUpdatesAtLeast(1)

	// With the temporary file created, copy Carol's current state into the
	// temporary file we created above. Later after more updates, we'll
	// restore this state.
	if err := net.BackupDb(carol); err != nil {
		t.Fatalf("unable to copy database files: %v", err)
	}

	// Finally, send payments from Dave to Carol, consuming Carol's
	// remaining payment hashes.
	err = completePaymentRequests(
		dave, dave.RouterClient, carolPayReqs[numInvoices/2:], false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// Ensure that carol's balance still shows the amount we originally
	// pushed to her (minus the HTLCs she sent to Bob), and that at least
	// one more update has occurred.
	time.Sleep(500 * time.Millisecond)
	checkCarolBalance(pushAmt - 3*paymentAmt)
	checkCarolNumUpdatesAtLeast(carolStateNumPreCopy + 1)

	// Disconnect Carol and Dave, so that the channel isn't corrected once Carol
	// is restarted.
	err = net.DisconnectNodes(dave, carol)
	if err != nil {
		t.Fatalf("unable to disconnect dave and carol: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// Suspend Dave to prevent him from sending a justice tx redeeming the
	// funds from the breach tx before Carol has a chance to redeem the
	// second level HTLCs.
	resumeDave, err := net.SuspendNode(dave)
	if err != nil {
		t.Fatalf("unable to suspend Dave: %v", err)
	}

	// Now we shutdown Carol, copying over the her temporary database state
	// which has the *prior* channel state over her current most up to date
	// state. With this, we essentially force Carol to travel back in time
	// within the channel's history.
	if err = net.RestartNode(carol, func() error {
		return net.RestoreDb(carol)
	}); err != nil {
		t.Fatalf("unable to restart node: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Ensure that Carol's view of the channel is consistent with the state
	// of the channel just before it was snapshotted.
	checkCarolBalance(pushAmt - 3*paymentAmt)
	checkCarolNumUpdatesAtLeast(1)

	// Now query for Carol's channel state, it should show that she's at a
	// state number in the past, *not* the latest state.
	carolChan, err = getChanInfo(carol)
	if err != nil {
		t.Fatalf("unable to get carol chan info: %v", err)
	}
	if carolChan.NumUpdates != carolStateNumPreCopy {
		t.Fatalf("db copy failed: %v", carolChan.NumUpdates)
	}

	// Now force Carol to execute a *force* channel closure by unilaterally
	// broadcasting her current channel state. This is actually the
	// commitment transaction of a prior *revoked* state, so she'll soon
	// feel the wrath of Dave's retribution.
	force := true
	closeUpdates, closeTxId, err := net.CloseChannel(carol, chanPoint, force)
	require.Nil(t.t, err)

	// Query the mempool for the breaching closing transaction, this should
	// be broadcast by Carol when she force closes the channel above.
	txid, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Carol's force close tx in mempool: %v",
			err)
	}
	if *txid != *closeTxId {
		t.Fatalf("expected closeTx(%v) in mempool, instead found %v",
			closeTxId, txid)
	}

	// Generate a single block to mine the breach transaction.
	block := mineBlocks(t, net, 1, 1)[0]

	// Wait for the final close status update, then ensure that the closing
	// transaction was included in the block.
	breachTXID, err := net.WaitForChannelClose(closeUpdates)
	require.Nil(t.t, err)
	if *breachTXID != *closeTxId {
		t.Fatalf("expected breach ID(%v) to be equal to close ID (%v)",
			breachTXID, closeTxId)
	}
	assertTxInBlock(t, block, breachTXID)

	// isSecondLevelSpend checks that the passed secondLevelTxid is a
	// potential second level spend spending from the commit tx.
	isSecondLevelSpend := func(commitTxid, secondLevelTxid *chainhash.Hash) (bool, *wire.MsgTx) {
		secondLevel, err := net.Miner.Node.GetRawTransaction(
			context.Background(), secondLevelTxid)
		if err != nil {
			t.Fatalf("unable to query for tx: %v", err)
		}

		// A second level spend should have only one input, and one
		// output.
		if len(secondLevel.MsgTx().TxIn) != 1 {
			return false, nil
		}
		if len(secondLevel.MsgTx().TxOut) != 1 {
			return false, nil
		}

		// The sole input should be spending from the commit tx.
		txIn := secondLevel.MsgTx().TxIn[0]
		fromCommit := bytes.Equal(txIn.PreviousOutPoint.Hash[:], commitTxid[:])
		if !fromCommit {
			return false, nil
		}

		return true, secondLevel.MsgTx()
	}

	// Grab transactions which will be required for future checks.
	breachTx, err := net.Miner.Node.GetRawTransaction(context.Background(), closeTxId)
	if err != nil {
		t.Fatalf("unable to query for breach tx: %v", err)
	}

	fundingTxId, err := chainhash.NewHash(chanPoint.GetFundingTxidBytes())
	if err != nil {
		t.Fatalf("chainpoint id bytes not a chainhash: %v", err)
	}
	fundingTx, err := net.Miner.Node.GetRawTransaction(context.Background(), fundingTxId)
	if err != nil {
		t.Fatalf("unable to query for funding tx: %v", err)
	}

	// After force-closing the channel, Carol should send 3 second-level
	// success htlc transactions to redeem her offered htlcs (for which she
	// has the preimage).
	//
	// We'll ensure she has actually sent them and record the fees to
	// correctly account for them in Dave's final balance.
	txids, err := waitForNTxsInMempool(net.Miner.Node, 3, minerMempoolTimeout)
	var numSecondLvlSuccess int
	var feesSecondLvlSuccess int64
	if err != nil {
		t.Fatalf("unable to wait for htlc success transactions: %v", err)
	}
	for _, txid := range txids {
		isSecondLevel, tx := isSecondLevelSpend(breachTXID, txid)
		if !isSecondLevel {
			continue
		}
		numSecondLvlSuccess++
		prevOut := breachTx.MsgTx().TxOut[tx.TxIn[0].PreviousOutPoint.Index]
		feesSecondLvlSuccess += prevOut.Value - tx.TxOut[0].Value
	}
	if numSecondLvlSuccess != 3 {
		t.Fatalf("Carol did not send the expected second-level htlc "+
			"success txs (found %d)", len(txids))
	}

	// Mine enough blocks for Carol to attempt to redeem the timed-out
	// htlcs via second-level timeout txs.
	numBlocks := padCLTV(uint32(cltvDelta - 1))
	mineBlocks(t, net, numBlocks, 0)

	// Wait for Carol to send the timeout second-level txs, then ensure
	// they can be found on the mempool, that they redeem from the breach
	// tx and that they are in fact second-level HTLC txs.
	mempool, err := waitForNTxsInMempool(net.Miner.Node, 3, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to get mempool from miner: %v", err)
	}
	var numSecondLvlTimeout int
	var totalSecondLvlTimeout int64
	var feesSecondLvlTimeout int64
	for _, txid := range mempool {
		isSecondLevel, tx := isSecondLevelSpend(breachTXID, txid)
		if !isSecondLevel {
			continue
		}
		prevOut := breachTx.MsgTx().TxOut[tx.TxIn[0].PreviousOutPoint.Index]
		numSecondLvlTimeout++
		totalSecondLvlTimeout += tx.TxOut[0].Value
		feesSecondLvlTimeout += prevOut.Value - tx.TxOut[0].Value
	}
	if numSecondLvlTimeout != 3 {
		t.Fatalf("unable to find the 3 htlc timeout second-level txs "+
			"from Carol (found %d)", numSecondLvlTimeout)
	}

	// The total redeemed by the second-level timeout txs should be the
	// amount for 3 time-locked invoices minus the fees.
	expectedSecondLvlTimeoutOut := 3*paymentAmt - feesSecondLvlTimeout
	if totalSecondLvlTimeout != expectedSecondLvlTimeoutOut {
		t.Fatalf("unexpected total redeemed by second-level txs; "+
			"expected=%d actual=%d fees=%d", expectedSecondLvlTimeoutOut,
			totalSecondLvlTimeout, feesSecondLvlTimeout)
	}

	// Mine a block with the timeout second-level HTLCs.
	mineBlocks(t, net, 1, 3)

	// Restart Dave. He should notice the breached commitment transaction
	// and the second-level txs. Once he does, he will send a justice tx to
	// sweep the time-locked funds from Carol's breach tx, his side of the
	// commitment tx, the time-locked 3 HTLCs in the success second-level
	// txs and the 3 time-locked HTLCs in the timeout second-level txs, for
	// a grand total of 8 inputs.
	err = resumeDave()
	if err != nil {
		t.Fatalf("unable to resume Dave: %v", err)
	}

	// Query the mempool for Dave's justice transaction.
	var predErr error
	var justiceTxid *chainhash.Hash
	exNumInputs := 2 + numInvoices
	errNotFound := fmt.Errorf("justice tx with %d inputs not found", exNumInputs)
	findJusticeTx := func() (*chainhash.Hash, error) {
		mempool, err := net.Miner.Node.GetRawMempool(context.Background(), jsonrpctypes.GRMRegular)
		if err != nil {
			return nil, fmt.Errorf("unable to get mempool from "+
				"miner: %v", err)
		}

		for _, txid := range mempool {
			// Check that the justice tx has the appropriate number
			// of inputs.
			tx, err := net.Miner.Node.GetRawTransaction(context.Background(), txid)
			if err != nil {
				return nil, fmt.Errorf("unable to query for "+
					"txs: %v", err)
			}

			if len(tx.MsgTx().TxIn) == exNumInputs {
				return txid, nil
			}
		}
		return nil, errNotFound
	}
	err = wait.Predicate(func() bool {
		txid, err := findJusticeTx()
		if err != nil {
			predErr = err
			return false
		}

		justiceTxid = txid
		return true
	}, defaultTimeout)
	if err != nil && predErr == errNotFound {
		// If Dave is unable to broadcast his justice tx on first
		// attempt because of the second layer transactions, he will
		// wait until the next block epoch before trying again. Because
		// of this, we'll mine a block if we cannot find the justice tx
		// immediately. Since we cannot tell for sure how many
		// transactions will be in the mempool at this point, we pass 0
		// as the last argument, indicating we don't care what's in the
		// mempool.
		mineBlocks(t, net, 1, 0)
		err = wait.Predicate(func() bool {
			txid, err := findJusticeTx()
			if err != nil {
				predErr = err
				return false
			}

			justiceTxid = txid
			return true
		}, defaultTimeout)
	}
	if err != nil {
		t.Fatalf(predErr.Error())
	}

	justiceTx, err := net.Miner.Node.GetRawTransaction(context.Background(), justiceTxid)
	if err != nil {
		t.Fatalf("unable to query for justice tx: %v", err)
	}

	// Check that all the inputs of this transaction are spending outputs
	// generated by Carol's breach transaction above or by one of the
	// second-level txs.
	var totalJusticeIn int64
	var justiceTxNumSecondLvlIn int
	for _, txIn := range justiceTx.MsgTx().TxIn {
		if bytes.Equal(txIn.PreviousOutPoint.Hash[:], breachTXID[:]) {
			txo := breachTx.MsgTx().TxOut[txIn.PreviousOutPoint.Index]
			totalJusticeIn += txo.Value
			continue
		}
		if is, tx := isSecondLevelSpend(breachTXID, &txIn.PreviousOutPoint.Hash); is {
			totalJusticeIn += tx.TxOut[0].Value
			justiceTxNumSecondLvlIn++
			continue
		}
		t.Fatalf("justice tx not spending commitment or second-level "+
			"utxo; instead is: %v", txIn.PreviousOutPoint)
	}
	if justiceTxNumSecondLvlIn != 6 {
		t.Fatalf("justice tx does not have 3 inputs from second-level "+
			"txs (found %d)", justiceTxNumSecondLvlIn)
	}

	// The amount returned by the justice tx must be the total channel
	// amount minus the (breached) commitment tx fee, the second-level fees
	// and the justice tx fee itself.
	jtx := justiceTx.MsgTx()
	commitFee := int64(calcStaticFee(cType, 6))
	justiceFee := totalJusticeIn - jtx.TxOut[0].Value
	expectedJusticeOut := int64(chanAmt) - commitFee - justiceFee -
		feesSecondLvlSuccess - feesSecondLvlTimeout
	if jtx.TxOut[0].Value != expectedJusticeOut {
		t.Fatalf("wrong value returned by justice tx; expected=%d "+
			"actual=%d chanAmt=%d commitFee=%d justiceFee=%d "+
			"secondLevelSuccessFees=%d secondLevelTimeoutFees=%d",
			expectedJusticeOut, jtx.TxOut[0].Value, chanAmt,
			commitFee, justiceFee, feesSecondLvlSuccess,
			feesSecondLvlTimeout)
	}

	// Now mine a block. This should include Dave's justice transaction
	// which was just accepted into the mempool.
	block = mineBlocks(t, net, 1, 1)[0]
	assertTxInBlock(t, block, justiceTxid)

	// Dave and Carol should have no open channels.
	assertNodeNumChannels(t, dave, 0)
	assertNodeNumChannels(t, carol, 0)

	// We'll now check that the total balance of each party is the one we
	// expect.  Introduce a new test closure.
	checkTotalBalance := func(node *lntest.HarnessNode, expectedAmt int64) {
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		balances, err := node.WalletBalance(ctxt, &lnrpc.WalletBalanceRequest{})
		if err != nil {
			t.Fatalf("unable to query node balance: %v", err)
		}
		if balances.TotalBalance != expectedAmt {
			t.Fatalf("balance is incorrect, "+
				"got %v, expected %v", balances.TotalBalance,
				expectedAmt)
		}
	}

	// Carol should not have any balance, because even though she was sent
	// coins via an HTLC, she chose to broadcast an older commitment tx and
	// forfeit her coins.
	checkTotalBalance(carol, 0)

	// Dave should have the initial amount sent to him for funding channels
	// minus the fees required to broadcast the breached commitment with 6
	// HTLCs and the justice tx.
	fundingFee := recordedTxFee(fundingTx.MsgTx())
	expectedDaveBalance := initialBalance - fundingFee - commitFee -
		feesSecondLvlSuccess - feesSecondLvlTimeout - justiceFee
	checkTotalBalance(dave, expectedDaveBalance)

}
