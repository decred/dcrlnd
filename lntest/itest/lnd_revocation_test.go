package itest

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	jsonrpctypes "github.com/decred/dcrd/rpc/jsonrpc/types/v4"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/watchtowerrpc"
	"github.com/decred/dcrlnd/lnrpc/wtclientrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntest/wait"
	"github.com/go-errors/errors"
)

// testRevokedCloseRetribution tests that Carol is able carry out
// retribution in the event that she fails immediately after detecting Bob's
// breach txn in the mempool.
func testRevokedCloseRetribution(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	const (
		chanAmt     = defaultChanAmt
		paymentAmt  = 10000
		numInvoices = 6
	)

	// Carol will be the breached party. We set --nolisten to ensure Bob
	// won't be able to connect to her and trigger the channel data
	// protection logic automatically. We also can't have Carol
	// automatically re-connect too early, otherwise DLP would be initiated
	// instead of the breach we want to provoke.
	carol := net.NewNode(
		t.t, "Carol",
		[]string{"--hodl.exit-settle", "--nolisten", "--minbackoff=1h"},
	)
	defer shutdownAndAssert(net, t, carol)

	// We must let Bob communicate with Carol before they are able to open
	// channel, so we connect Bob and Carol,
	net.ConnectNodes(t.t, carol, net.Bob)

	// Before we make a channel, we'll load up Carol with some coins sent
	// directly from the miner.
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	// In order to test Carol's response to an uncooperative channel
	// closure by Bob, we'll first open up a channel between them with a
	// 0.5 DCR value.
	chanPoint := openChannelAndAssert(
		t, net, carol, net.Bob,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// With the channel open, we'll create a few invoices for Bob that
	// Carol will pay to in order to advance the state of the channel.
	bobPayReqs, _, _, err := createPayReqs(
		net.Bob, paymentAmt, numInvoices,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// Wait for Carol to receive the channel edge from the funding manager.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = carol.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("carol didn't see the carol->bob channel before "+
			"timeout: %v", err)
	}

	// Send payments from Carol to Bob using 3 of Bob's payment hashes
	// generated above.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, carol, carol.RouterClient, bobPayReqs[:numInvoices/2],
		true,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// Next query for Bob's channel state, as we sent 3 payments of 10k
	// atoms each, Bob should now see his balance as being 30k atoms.
	var bobChan *lnrpc.Channel
	var predErr error
	err = wait.Predicate(func() bool {
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		bChan, err := getChanInfo(ctxt, net.Bob)
		if err != nil {
			t.Fatalf("unable to get bob's channel info: %v", err)
		}
		if bChan.LocalBalance != 30000 {
			predErr = fmt.Errorf("bob's balance is incorrect, "+
				"got %v, expected %v", bChan.LocalBalance,
				30000)
			return false
		}

		bobChan = bChan
		return true
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("%v", predErr)
	}

	// Grab Bob's current commitment height (update number), we'll later
	// revert him to this state after additional updates to force him to
	// broadcast this soon to be revoked state.
	bobStateNumPreCopy := bobChan.NumUpdates

	// With the temporary file created, copy Bob's current state into the
	// temporary file we created above. Later after more updates, we'll
	// restore this state.
	if err := net.BackupDb(net.Bob); err != nil {
		t.Fatalf("unable to copy database files: %v", err)
	}

	// Finally, send payments from Carol to Bob, consuming Bob's remaining
	// payment hashes.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, carol, carol.RouterClient, bobPayReqs[numInvoices/2:],
		true,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	bobChan, err = getChanInfo(ctxt, net.Bob)
	if err != nil {
		t.Fatalf("unable to get bob chan info: %v", err)
	}

	// Disconnect the nodes to prevent Carol trying to reconnect to Bob after
	// Bob is restarted and fixing the wrong state.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.DisconnectNodes(ctxt, carol, net.Bob)
	if err != nil {
		t.Fatalf("unable to disconnect carol and bob: %v", err)
	}

	// Now we shutdown Bob, copying over the his temporary database state
	// which has the *prior* channel state over his current most up to date
	// state. With this, we essentially force Bob to travel back in time
	// within the channel's history.
	if err = net.RestartNode(net.Bob, func() error {
		return net.RestoreDb(net.Bob)
	}); err != nil {
		t.Fatalf("unable to restart node: %v", err)
	}

	// Now query for Bob's channel state, it should show that he's at a
	// state number in the past, not the *latest* state.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	bobChan, err = getChanInfo(ctxt, net.Bob)
	if err != nil {
		t.Fatalf("unable to get bob chan info: %v", err)
	}
	if bobChan.NumUpdates != bobStateNumPreCopy {
		t.Fatalf("db copy failed: %v", bobChan.NumUpdates)
	}

	// Now force Bob to execute a *force* channel closure by unilaterally
	// broadcasting his current channel state. This is actually the
	// commitment transaction of a prior *revoked* state, so he'll soon
	// feel the wrath of Carol's retribution.
	var closeUpdates lnrpc.Lightning_CloseChannelClient
	force := true
	err = wait.Predicate(func() bool {
		ctxt, _ := context.WithTimeout(ctxb, channelCloseTimeout)
		closeUpdates, _, err = net.CloseChannel(ctxt, net.Bob, chanPoint, force)
		if err != nil {
			predErr = err
			return false
		}

		return true
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("unable to close channel: %v", predErr)
	}

	// Wait for Bob's breach transaction to show up in the mempool to ensure
	// that Carol's node has started waiting for confirmations.
	_, err = waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Bob's breach tx in mempool: %v", err)
	}

	// Here, Carol sees Bob's breach transaction in the mempool, but is waiting
	// for it to confirm before continuing her retribution. We restart Carol to
	// ensure that she is persisting her retribution state and continues
	// watching for the breach transaction to confirm even after her node
	// restarts.
	if err := net.RestartNode(carol, nil); err != nil {
		t.Fatalf("unable to restart Carol's node: %v", err)
	}

	// Finally, generate a single block, wait for the final close status
	// update, then ensure that the closing transaction was included in the
	// block.
	block := mineBlocks(t, net, 1, 1)[0]

	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	breachTXID, err := net.WaitForChannelClose(ctxt, closeUpdates)
	if err != nil {
		t.Fatalf("error while waiting for channel close: %v", err)
	}
	assertTxInBlock(t, block, breachTXID)

	// Query the mempool for Carol's justice transaction, this should be
	// broadcast as Bob's contract breaching transaction gets confirmed
	// above.
	justiceTXID, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Carol's justice tx in mempool: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Query for the mempool transaction found above. Then assert that all
	// the inputs of this transaction are spending outputs generated by
	// Bob's breach transaction above.
	justiceTx, err := net.Miner.Node.GetRawTransaction(context.Background(), justiceTXID)
	if err != nil {
		t.Fatalf("unable to query for justice tx: %v", err)
	}
	for _, txIn := range justiceTx.MsgTx().TxIn {
		if !bytes.Equal(txIn.PreviousOutPoint.Hash[:], breachTXID[:]) {
			t.Fatalf("justice tx not spending commitment utxo "+
				"instead is: %v", txIn.PreviousOutPoint)
		}
	}

	// We restart Carol here to ensure that she persists her retribution state
	// and successfully continues exacting retribution after restarting. At
	// this point, Carol has broadcast the justice transaction, but it hasn't
	// been confirmed yet; when Carol restarts, she should start waiting for
	// the justice transaction to confirm again.
	if err := net.RestartNode(carol, nil); err != nil {
		t.Fatalf("unable to restart Carol's node: %v", err)
	}

	// Now mine a block, this transaction should include Carol's justice
	// transaction which was just accepted into the mempool.
	block = mineBlocks(t, net, 1, 1)[0]

	// The block should have exactly *two* transactions, one of which is
	// the justice transaction.
	if len(block.Transactions) != 2 {
		t.Fatalf("transaction wasn't mined")
	}
	justiceSha := block.Transactions[1].TxHash()
	if !bytes.Equal(justiceTx.Hash()[:], justiceSha[:]) {
		t.Fatalf("justice tx wasn't mined")
	}

	assertNodeNumChannels(t, carol, 0)

	// Mine enough blocks for Bob's channel arbitrator to wrap up the
	// references to the breached channel. The chanarb waits for commitment
	// tx's confHeight+CSV-1 blocks and since we've already mined one that
	// included the justice tx we only need to mine extra DefaultCSV-2
	// blocks to unlock it.
	mineBlocks(t, net, lntest.DefaultCSV-2, 0)

	assertNumPendingChannels(t, net.Bob, 0, 0, 0, 0)
}

// testRevokedCloseRetributionZeroValueRemoteOutput tests that Dave is able
// carry out retribution in the event that she fails in state where the remote
// commitment output has zero-value.
func testRevokedCloseRetributionZeroValueRemoteOutput(net *lntest.NetworkHarness,
	t *harnessTest) {
	ctxb := context.Background()

	const (
		chanAmt     = defaultChanAmt
		paymentAmt  = 10000
		numInvoices = 6
	)

	// Since we'd like to test some multi-hop failure scenarios, we'll
	// introduce another node into our test network: Carol.
	carol := net.NewNode(t.t, "Carol", []string{"--hodl.exit-settle"})
	defer shutdownAndAssert(net, t, carol)

	// Dave will be the breached party. We set --nolisten to ensure Carol
	// won't be able to connect to him and trigger the channel data
	// protection logic automatically. We also can't have Dave automatically
	// re-connect too early, otherwise DLP would be initiated instead of the
	// breach we want to provoke.
	dave := net.NewNode(
		t.t, "Dave",
		[]string{"--hodl.exit-settle", "--nolisten", "--minbackoff=1h"},
	)
	defer shutdownAndAssert(net, t, dave)

	// We must let Dave have an open channel before she can send a node
	// announcement, so we open a channel with Carol,
	net.ConnectNodes(t.t, dave, carol)

	// Before we make a channel, we'll load up Dave with some coins sent
	// directly from the miner.
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, dave)

	// In order to test Dave's response to an uncooperative channel
	// closure by Carol, we'll first open up a channel between them with a
	// 0.5 DCR value.
	chanPoint := openChannelAndAssert(
		t, net, dave, carol,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// With the channel open, we'll create a few invoices for Carol that
	// Dave will pay to in order to advance the state of the channel.
	carolPayReqs, _, _, err := createPayReqs(
		carol, paymentAmt, numInvoices,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// Wait for Dave to receive the channel edge from the funding manager.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = dave.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("dave didn't see the dave->carol channel before "+
			"timeout: %v", err)
	}

	// Next query for Carol's channel state, as we sent 0 payments, Carol
	// should now see her balance as being 0 atoms.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err := getChanInfo(ctxt, carol)
	if err != nil {
		t.Fatalf("unable to get carol's channel info: %v", err)
	}
	if carolChan.LocalBalance != 0 {
		t.Fatalf("carol's balance is incorrect, got %v, expected %v",
			carolChan.LocalBalance, 0)
	}

	// Grab Carol's current commitment height (update number), we'll later
	// revert her to this state after additional updates to force him to
	// broadcast this soon to be revoked state.
	carolStateNumPreCopy := carolChan.NumUpdates

	// With the temporary file created, copy Carol's current state into the
	// temporary file we created above. Later after more updates, we'll
	// restore this state.
	if err := net.BackupDb(carol); err != nil {
		t.Fatalf("unable to copy database files: %v", err)
	}

	// Finally, send payments from Dave to Carol, consuming Carol's remaining
	// payment hashes.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, dave, dave.RouterClient, carolPayReqs, false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	_, err = getChanInfo(ctxt, carol)
	if err != nil {
		t.Fatalf("unable to get carol chan info: %v", err)
	}

	// Disconnect Dave from Carol, so that upon Carol's restart he doesn't
	// try to automatically reconnect and alert her of the changed state.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.DisconnectNodes(ctxt, dave, carol)
	if err != nil {
		t.Fatalf("unable to disconnect dave and carol: %v", err)
	}

	// Now we shutdown Carol, copying over the his temporary database state
	// which has the *prior* channel state over his current most up to date
	// state. With this, we essentially force Carol to travel back in time
	// within the channel's history.
	if err = net.RestartNode(carol, func() error {
		return net.RestoreDb(carol)
	}); err != nil {
		t.Fatalf("unable to restart node: %v", err)
	}

	// Now query for Carol's channel state, it should show that he's at a
	// state number in the past, not the *latest* state.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err = getChanInfo(ctxt, carol)
	if err != nil {
		t.Fatalf("unable to get carol chan info: %v", err)
	}
	if carolChan.NumUpdates != carolStateNumPreCopy {
		t.Fatalf("db copy failed: %v", carolChan.NumUpdates)
	}

	// Now force Carol to execute a *force* channel closure by unilaterally
	// broadcasting his current channel state. This is actually the
	// commitment transaction of a prior *revoked* state, so he'll soon
	// feel the wrath of Dave's retribution.
	var (
		closeUpdates lnrpc.Lightning_CloseChannelClient
		closeTxID    *chainhash.Hash
		closeErr     error
	)

	force := true
	err = wait.Predicate(func() bool {
		ctxt, _ := context.WithTimeout(ctxb, channelCloseTimeout)
		closeUpdates, closeTxID, closeErr = net.CloseChannel(
			ctxt, carol, chanPoint, force,
		)
		return closeErr == nil
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("unable to close channel: %v", closeErr)
	}

	// Query the mempool for the breaching closing transaction, this should
	// be broadcast by Carol when she force closes the channel above.
	txid, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Carol's force close tx in mempool: %v",
			err)
	}
	if *txid != *closeTxID {
		t.Fatalf("expected closeTx(%v) in mempool, instead found %v",
			closeTxID, txid)
	}

	// Finally, generate a single block, wait for the final close status
	// update, then ensure that the closing transaction was included in the
	// block.
	block := mineBlocks(t, net, 1, 1)[0]

	// Give Dave some time to process and broadcast the brach transaction
	time.Sleep(time.Second * 3)

	// Here, Dave receives a confirmation of Carol's breach transaction.
	// We restart Dave to ensure that she is persisting her retribution
	// state and continues exacting justice after her node restarts.
	if err := net.RestartNode(dave, nil); err != nil {
		t.Fatalf("unable to stop Dave's node: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	breachTXID, err := net.WaitForChannelClose(ctxt, closeUpdates)
	if err != nil {
		t.Fatalf("error while waiting for channel close: %v", err)
	}
	assertTxInBlock(t, block, breachTXID)

	// Query the mempool for Dave's justice transaction, this should be
	// broadcast as Carol's contract breaching transaction gets confirmed
	// above.
	justiceTXID, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Dave's justice tx in mempool: %v",
			err)
	}
	time.Sleep(100 * time.Millisecond)

	// Query for the mempool transaction found above. Then assert that all
	// the inputs of this transaction are spending outputs generated by
	// Carol's breach transaction above.
	justiceTx, err := net.Miner.Node.GetRawTransaction(context.Background(), justiceTXID)
	if err != nil {
		t.Fatalf("unable to query for justice tx: %v", err)
	}
	for _, txIn := range justiceTx.MsgTx().TxIn {
		if !bytes.Equal(txIn.PreviousOutPoint.Hash[:], breachTXID[:]) {
			t.Fatalf("justice tx not spending commitment utxo "+
				"instead is: %v", txIn.PreviousOutPoint)
		}
	}

	// We restart Dave here to ensure that he persists her retribution state
	// and successfully continues exacting retribution after restarting. At
	// this point, Dave has broadcast the justice transaction, but it hasn't
	// been confirmed yet; when Dave restarts, she should start waiting for
	// the justice transaction to confirm again.
	if err := net.RestartNode(dave, nil); err != nil {
		t.Fatalf("unable to restart Dave's node: %v", err)
	}

	// Now mine a block, this transaction should include Dave's justice
	// transaction which was just accepted into the mempool.
	block = mineBlocks(t, net, 1, 1)[0]

	// The block should have exactly *two* transactions, one of which is
	// the justice transaction.
	if len(block.Transactions) != 2 {
		t.Fatalf("transaction wasn't mined")
	}
	justiceSha := block.Transactions[1].TxHash()
	if !bytes.Equal(justiceTx.Hash()[:], justiceSha[:]) {
		t.Fatalf("justice tx wasn't mined")
	}

	assertNodeNumChannels(t, dave, 0)
}

// testRevokedCloseRetributionRemoteHodl tests that Dave properly responds to a
// channel breach made by the remote party, specifically in the case that the
// remote party breaches before settling extended HTLCs.
//
// This test specifically suspends Carol after she sends her breach commitment
// so that we test the scenario where she does _not_ go to the second level
// before Dave is able to exact justice.
func testRevokedCloseRetributionRemoteHodl(net *lntest.NetworkHarness,
	t *harnessTest) {
	ctxb := context.Background()

	const (
		initialBalance = int64(dcrutil.AtomsPerCoin)
		chanAmt        = defaultChanAmt
		pushAmt        = 400000
		paymentAmt     = 20000
		numInvoices    = 6
	)

	// Since this test will result in the counterparty being left in a
	// weird state, we will introduce another node into our test network:
	// Carol.
	carol := net.NewNode(t.t, "Carol", []string{"--hodl.exit-settle"})
	defer shutdownAndAssert(net, t, carol)

	// We'll also create a new node Dave, who will have a channel with
	// Carol, and also use similar settings so we can broadcast a commit
	// with active HTLCs. Dave will be the breached party. We set
	// --nolisten to ensure Carol won't be able to connect to him and
	// trigger the channel data protection logic automatically.
	dave := net.NewNode(
		t.t, "Dave",
		[]string{"--hodl.exit-settle", "--nolisten"},
	)
	defer shutdownAndAssert(net, t, dave)

	// We must let Dave communicate with Carol before they are able to open
	// channel, so we connect Dave and Carol,
	net.ConnectNodes(t.t, dave, carol)

	// Before we make a channel, we'll load up Dave with some coins sent
	// directly from the miner.
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, dave)

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
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		carolChan, err := getChanInfo(ctxt, carol)
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
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		carolChan, err := getChanInfo(ctxt, carol)
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = dave.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("dave didn't see the dave->carol channel before "+
			"timeout: %v", err)
	}

	// Ensure that carol's balance starts with the amount we pushed to her.
	checkCarolBalance(pushAmt)

	// Send payments from Dave to Carol using 3 of Carol's payment hashes
	// generated above.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, dave, dave.RouterClient, carolPayReqs[:numInvoices/2],
		false,
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, carol, carol.RouterClient, davePayReqs[:numInvoices/2],
		false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	// Wait until the channel has acknowledged the pending htlc count.
	err = waitForPendingHtlcs(carol, chanPoint, numInvoices)
	if err != nil {
		t.Fatalf("unable to wait for pending htlcs: %v", err)
	}

	// Next query for Carol's channel state, as we sent 3 payments of 10k
	// atoms each, however Carol should now see her balance as being
	// equal to the push amount in atoms since she has not settled.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err := getChanInfo(ctxt, carol)
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, dave, dave.RouterClient, carolPayReqs[numInvoices/2:],
		false,
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.DisconnectNodes(ctxt, dave, carol)
	if err != nil {
		t.Fatalf("unable to disconnect dave and carol: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err = getChanInfo(ctxt, carol)
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
	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	_, closeTxID, err := net.CloseChannel(ctxt, carol,
		chanPoint, force)
	if err != nil {
		t.Fatalf("unable to close channel: %v", err)
	}

	// Query the mempool for the breaching closing transaction, this should
	// be broadcast by Carol when she force closes the channel above.
	txid, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Carol's force close tx in mempool: %v",
			err)
	}
	if *txid != *closeTxID {
		t.Fatalf("expected closeTx(%v) in mempool, instead found %v",
			closeTxID, txid)
	}
	time.Sleep(200 * time.Millisecond)

	// Suspend Carol, so that she won't have a chance to go to the second
	// level to redeem the coins before Dave sees the breach tx.
	resumeCarol, err := net.SuspendNode(carol)
	if err != nil {
		t.Fatalf("unable to suspend carol: %v", err)
	}

	// Generate a single block to mine the breach transaction and ensure
	// that it was mined.
	block := mineBlocks(t, net, 1, 1)[0]
	assertTxInBlock(t, block, closeTxID)

	// Wait so Dave receives a confirmation of Carol's breach transaction.
	time.Sleep(2 * time.Second)

	// We restart Dave to ensure that he is persisting his retribution
	// state and continues exacting justice after her node restarts.
	if err := net.RestartNode(dave, nil); err != nil {
		t.Fatalf("unable to stop Dave's node: %v", err)
	}

	// Query the mempool for Dave's justice transaction. This should be
	// broadcast as Carol's contract breaching transaction gets confirmed
	// above. To find it, we'll search through the mempool for a tx that
	// matches the number of expected inputs in the justice tx.
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
	}, time.Second*10)
	if err != nil {
		t.Fatalf("unable to find dave's justice tx: %v", predErr)
	}

	// Grab some transactions which will be needed for future checks.
	justiceTx, err := net.Miner.Node.GetRawTransaction(context.Background(), justiceTxid)
	if err != nil {
		t.Fatalf("unable to query for justice tx: %v", err)
	}

	breachTx, err := net.Miner.Node.GetRawTransaction(context.Background(), closeTxID)
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

	// Check that all the inputs of the justice transaction are spending
	// outputs generated by Carol's breach transaction above. We'll track
	// the total amount sent into the justice tx so we can check for Dave's
	// balance later on.
	var totalJusticeIn int64
	for _, txIn := range justiceTx.MsgTx().TxIn {
		if bytes.Equal(txIn.PreviousOutPoint.Hash[:], closeTxID[:]) {
			txo := breachTx.MsgTx().TxOut[txIn.PreviousOutPoint.Index]
			totalJusticeIn += txo.Value
			continue
		}

		t.Fatalf("justice tx not spending commitment utxo "+
			"instead is: %v", txIn.PreviousOutPoint)
	}

	// The amount returned by the justice tx must be the total channel
	// amount minus the (breached) commitment tx fee and the justice tx fee
	// itself.
	jtx := justiceTx.MsgTx()
	commitFee := int64(cType.calcStaticFee(6))
	justiceFee := totalJusticeIn - jtx.TxOut[0].Value
	expectedJusticeOut := int64(chanAmt) - commitFee - justiceFee
	if jtx.TxOut[0].Value != expectedJusticeOut {
		t.Fatalf("wrong value returned by justice tx; expected=%d "+
			"actual=%d chanAmt=%d commitFee=%d justiceFee=%d",
			expectedJusticeOut, jtx.TxOut[0].Value, chanAmt,
			commitFee, justiceFee)
	}

	// Restart Carol. She should not be able to do anything else regarding
	// the breached channel.
	err = resumeCarol()
	if err != nil {
		t.Fatalf("unable to resume Carol: %v", err)
	}

	// We restart Dave here to ensure that he persists he retribution state
	// and successfully continues exacting retribution after restarting. At
	// this point, Dave has broadcast the justice transaction, but it
	// hasn't been confirmed yet; when Dave restarts, he should start
	// waiting for the justice transaction to confirm again.
	if err := net.RestartNode(dave, nil); err != nil {
		t.Fatalf("unable to restart Dave's node: %v", err)
	}

	// Now mine a block. This should include Dave's justice transaction
	// which was just accepted into the mempool.
	block = mineBlocks(t, net, 1, 1)[0]
	assertTxInBlock(t, block, justiceTxid)

	// Dave and Carol should have no open channels.
	assertNodeNumChannels(t, dave, 0)
	assertNodeNumChannels(t, carol, 0)

	// We'll now check that the total balance of each party is the one we
	// expect. Introduce a new test closure.
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

	// Dave should the initial amount sent to him for funding channels
	// minus the fees required to broadcast the breached commitment with 6
	// HTLCs and the justice tx.
	fundingFee := recordedTxFee(fundingTx.MsgTx())
	checkTotalBalance(dave, initialBalance-commitFee-justiceFee-fundingFee)
}

// testRevokedCloseRetributionAltruistWatchtower establishes a channel between
// Carol and Dave, where Carol is using a third node Willy as her watchtower.
// After sending some payments, Dave reverts his state and force closes to
// trigger a breach. Carol is kept offline throughout the process and the test
// asserts that Willy responds by broadcasting the justice transaction on
// Carol's behalf sweeping her funds without a reward.
func testRevokedCloseRetributionAltruistWatchtower(net *lntest.NetworkHarness,
	t *harnessTest) {

	testCases := []struct {
		name    string
		anchors bool
	}{{
		name:    "anchors",
		anchors: true,
	}, {
		name:    "legacy",
		anchors: false,
	}}

	for _, tc := range testCases {
		tc := tc

		success := t.t.Run(tc.name, func(tt *testing.T) {
			ht := newHarnessTest(tt, net)
			ht.RunTestCase(&testCase{
				name: tc.name,
				test: func(net1 *lntest.NetworkHarness, t1 *harnessTest) {
					testRevokedCloseRetributionAltruistWatchtowerCase(
						net1, t1, tc.anchors,
					)
				},
			})
		})

		if !success {
			// Log failure time to help relate the lnd logs to the
			// failure.
			t.Logf("Failure time: %v", time.Now().Format(
				"2006-01-02 15:04:05.000",
			))

			break
		}
	}
}

func testRevokedCloseRetributionAltruistWatchtowerCase(
	net *lntest.NetworkHarness, t *harnessTest, anchors bool) {

	ctxb := context.Background()
	const (
		chanAmt     = defaultChanAmt
		paymentAmt  = 10000
		numInvoices = 6
		externalIP  = "1.2.3.4"
	)

	// Since we'd like to test some multi-hop failure scenarios, we'll
	// introduce another node into our test network: Carol.
	carolArgs := []string{"--hodl.exit-settle"}
	if anchors {
		carolArgs = append(carolArgs, "--protocol.anchors")
	}
	carol := net.NewNode(t.t, "Carol", carolArgs)
	defer shutdownAndAssert(net, t, carol)

	// Willy the watchtower will protect Dave from Carol's breach. He will
	// remain online in order to punish Carol on Dave's behalf, since the
	// breach will happen while Dave is offline.
	willy := net.NewNode(t.t, "Willy", []string{
		"--watchtower.active",
		"--watchtower.externalip=" + externalIP,
	})
	defer shutdownAndAssert(net, t, willy)

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	willyInfo, err := willy.Watchtower.GetInfo(
		ctxt, &watchtowerrpc.GetInfoRequest{},
	)
	if err != nil {
		t.Fatalf("unable to getinfo from willy: %v", err)
	}

	// Assert that Willy has one listener and it is 0.0.0.0:9911 or
	// [::]:9911. Since no listener is explicitly specified, one of these
	// should be the default depending on whether the host supports IPv6 or
	// not.
	if len(willyInfo.Listeners) != 1 {
		t.Fatalf("Willy should have 1 listener, has %d",
			len(willyInfo.Listeners))
	}
	listener := willyInfo.Listeners[0]
	if listener != "0.0.0.0:9911" && listener != "[::]:9911" {
		t.Fatalf("expected listener on 0.0.0.0:9911 or [::]:9911, "+
			"got %v", listener)
	}

	// Assert the Willy's URIs properly display the chosen external IP.
	if len(willyInfo.Uris) != 1 {
		t.Fatalf("Willy should have 1 uri, has %d",
			len(willyInfo.Uris))
	}
	if !strings.Contains(willyInfo.Uris[0], externalIP) {
		t.Fatalf("expected uri with %v, got %v",
			externalIP, willyInfo.Uris[0])
	}

	// Dave will be the breached party. We set --nolisten to ensure Carol
	// won't be able to connect to him and trigger the channel data
	// protection logic automatically.
	daveArgs := []string{
		"--nolisten",
		"--wtclient.active",
	}
	if anchors {
		daveArgs = append(daveArgs, "--protocol.anchors")
	}
	dave := net.NewNode(t.t, "Dave", daveArgs)
	defer shutdownAndAssert(net, t, dave)

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	addTowerReq := &wtclientrpc.AddTowerRequest{
		Pubkey:  willyInfo.Pubkey,
		Address: listener,
	}
	if _, err = dave.WatchtowerClient.AddTower(ctxt, addTowerReq); err != nil {
		t.Fatalf("unable to add willy's watchtower: %v", err)
	}

	// We must let Dave have an open channel before she can send a node
	// announcement, so we open a channel with Carol,
	net.ConnectNodes(t.t, dave, carol)

	// Before we make a channel, we'll load up Dave with some coins sent
	// directly from the miner.
	net.SendCoins(ctxb, t.t, dcrutil.AtomsPerCoin, dave)

	// In order to test Dave's response to an uncooperative channel
	// closure by Carol, we'll first open up a channel between them with a
	// 0.5 DCR value.
	chanPoint := openChannelAndAssert(
		t, net, dave, carol,
		lntest.OpenChannelParams{
			Amt:     3 * (chanAmt / 4),
			PushAmt: chanAmt / 4,
		},
	)

	// With the channel open, we'll create a few invoices for Carol that
	// Dave will pay to in order to advance the state of the channel.
	carolPayReqs, _, _, err := createPayReqs(
		carol, paymentAmt, numInvoices,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// Wait for Dave to receive the channel edge from the funding manager.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = dave.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("dave didn't see the dave->carol channel before "+
			"timeout: %v", err)
	}

	// Next query for Carol's channel state, as we sent 0 payments, Carol
	// should still see her balance as the push amount, which is 1/4 of the
	// capacity.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err := getChanInfo(ctxt, carol)
	if err != nil {
		t.Fatalf("unable to get carol's channel info: %v", err)
	}
	if carolChan.LocalBalance != int64(chanAmt/4) {
		t.Fatalf("carol's balance is incorrect, got %v, expected %v",
			carolChan.LocalBalance, chanAmt/4)
	}

	// Grab Carol's current commitment height (update number), we'll later
	// revert her to this state after additional updates to force him to
	// broadcast this soon to be revoked state.
	carolStateNumPreCopy := carolChan.NumUpdates

	// With the temporary file created, copy Carol's current state into the
	// temporary file we created above. Later after more updates, we'll
	// restore this state.
	if err := net.BackupDb(carol); err != nil {
		t.Fatalf("unable to copy database files: %v", err)
	}

	// Finally, send payments from Dave to Carol, consuming Carol's remaining
	// payment hashes.
	err = completePaymentRequests(
		ctxb, dave, dave.RouterClient, carolPayReqs, false,
	)
	if err != nil {
		t.Fatalf("unable to send payments: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	daveBalReq := &lnrpc.WalletBalanceRequest{}
	daveBalResp, err := dave.WalletBalance(ctxt, daveBalReq)
	if err != nil {
		t.Fatalf("unable to get dave's balance: %v", err)
	}

	davePreSweepBalance := daveBalResp.ConfirmedBalance

	// Wait until the backup has been accepted by the watchtower before
	// shutting down Dave.
	err = wait.NoError(func() error {
		ctxt, cancel := context.WithTimeout(ctxb, defaultTimeout)
		defer cancel()
		bkpStats, err := dave.WatchtowerClient.Stats(ctxt,
			&wtclientrpc.StatsRequest{},
		)
		if err != nil {
			return err
		}
		if bkpStats == nil {
			return errors.New("no active backup sessions")
		}
		if bkpStats.NumBackups == 0 {
			return errors.New("no backups accepted")
		}
		return nil
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("unable to verify backup task completed: %v", err)
	}

	// Shutdown Dave to simulate going offline for an extended period of
	// time. Once he's not watching, Carol will try to breach the channel.
	restart, err := net.SuspendNode(dave)
	if err != nil {
		t.Fatalf("unable to suspend Dave: %v", err)
	}

	// Now we shutdown Carol, copying over the his temporary database state
	// which has the *prior* channel state over his current most up to date
	// state. With this, we essentially force Carol to travel back in time
	// within the channel's history.
	if err = net.RestartNode(carol, func() error {
		return net.RestoreDb(carol)
	}); err != nil {
		t.Fatalf("unable to restart node: %v", err)
	}

	// Now query for Carol's channel state, it should show that he's at a
	// state number in the past, not the *latest* state.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	carolChan, err = getChanInfo(ctxt, carol)
	if err != nil {
		t.Fatalf("unable to get carol chan info: %v", err)
	}
	if carolChan.NumUpdates != carolStateNumPreCopy {
		t.Fatalf("db copy failed: %v", carolChan.NumUpdates)
	}

	// Now force Carol to execute a *force* channel closure by unilaterally
	// broadcasting his current channel state. This is actually the
	// commitment transaction of a prior *revoked* state, so he'll soon
	// feel the wrath of Dave's retribution.
	closeUpdates, closeTxID, err := net.CloseChannel(
		ctxb, carol, chanPoint, true,
	)
	if err != nil {
		t.Fatalf("unable to close channel: %v", err)
	}

	// Query the mempool for the breaching closing transaction, this should
	// be broadcast by Carol when she force closes the channel above.
	txid, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Carol's force close tx in mempool: %v",
			err)
	}
	if *txid != *closeTxID {
		t.Fatalf("expected closeTx(%v) in mempool, instead found %v",
			closeTxID, txid)
	}

	// Finally, generate a single block, wait for the final close status
	// update, then ensure that the closing transaction was included in the
	// block.
	block := mineBlocks(t, net, 1, 1)[0]

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	breachTXID, err := net.WaitForChannelClose(ctxt, closeUpdates)
	if err != nil {
		t.Fatalf("error while waiting for channel close: %v", err)
	}
	assertTxInBlock(t, block, breachTXID)

	// Query the mempool for Dave's justice transaction, this should be
	// broadcast as Carol's contract breaching transaction gets confirmed
	// above.
	justiceTXID, err := waitForTxInMempool(net.Miner.Node, minerMempoolTimeout)
	if err != nil {
		t.Fatalf("unable to find Dave's justice tx in mempool: %v",
			err)
	}
	time.Sleep(100 * time.Millisecond)

	// Query for the mempool transaction found above. Then assert that all
	// the inputs of this transaction are spending outputs generated by
	// Carol's breach transaction above.
	justiceTx, err := net.Miner.Node.GetRawTransaction(context.Background(), justiceTXID)
	if err != nil {
		t.Fatalf("unable to query for justice tx: %v", err)
	}
	for _, txIn := range justiceTx.MsgTx().TxIn {
		if !bytes.Equal(txIn.PreviousOutPoint.Hash[:], breachTXID[:]) {
			t.Fatalf("justice tx not spending commitment utxo "+
				"instead is: %v", txIn.PreviousOutPoint)
		}
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	willyBalReq := &lnrpc.WalletBalanceRequest{}
	willyBalResp, err := willy.WalletBalance(ctxt, willyBalReq)
	if err != nil {
		t.Fatalf("unable to get willy's balance: %v", err)
	}

	if willyBalResp.ConfirmedBalance != 0 {
		t.Fatalf("willy should have 0 balance before mining "+
			"justice transaction, instead has %d",
			willyBalResp.ConfirmedBalance)
	}

	// Now mine a block, this transaction should include Dave's justice
	// transaction which was just accepted into the mempool.
	block = mineBlocks(t, net, 1, 1)[0]

	// The block should have exactly *two* transactions, one of which is
	// the justice transaction.
	if len(block.Transactions) != 2 {
		t.Fatalf("transaction wasn't mined")
	}
	justiceSha := block.Transactions[1].TxHash()
	if !bytes.Equal(justiceTx.Hash()[:], justiceSha[:]) {
		t.Fatalf("justice tx wasn't mined")
	}

	// Ensure that Willy doesn't get any funds, as he is acting as an
	// altruist watchtower.
	var predErr error
	err = wait.Invariant(func() bool {
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		willyBalReq := &lnrpc.WalletBalanceRequest{}
		willyBalResp, err := willy.WalletBalance(ctxt, willyBalReq)
		if err != nil {
			t.Fatalf("unable to get willy's balance: %v", err)
		}

		if willyBalResp.ConfirmedBalance != 0 {
			predErr = fmt.Errorf("Expected Willy to have no funds "+
				"after justice transaction was mined, found %v",
				willyBalResp)
			return false
		}

		return true
	}, time.Second*5)
	if err != nil {
		t.Fatalf("%v", predErr)
	}

	// Restart Dave, who will still think his channel with Carol is open.
	// We should him to detect the breach, but realize that the funds have
	// then been swept to his wallet by Willy.
	err = restart()
	if err != nil {
		t.Fatalf("unable to restart dave: %v", err)
	}

	err = wait.Predicate(func() bool {
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		daveBalReq := &lnrpc.ChannelBalanceRequest{}
		daveBalResp, err := dave.ChannelBalance(ctxt, daveBalReq)
		if err != nil {
			t.Fatalf("unable to get dave's balance: %v", err)
		}

		if daveBalResp.LocalBalance.Atoms != 0 {
			predErr = fmt.Errorf("Dave should end up with zero "+
				"channel balance, instead has %d",
				daveBalResp.LocalBalance.Atoms)
			return false
		}

		return true
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("%v", predErr)
	}

	assertNumPendingChannels(t, dave, 0, 0, 0, 0)

	err = wait.Predicate(func() bool {
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		daveBalReq := &lnrpc.WalletBalanceRequest{}
		daveBalResp, err := dave.WalletBalance(ctxt, daveBalReq)
		if err != nil {
			t.Fatalf("unable to get dave's balance: %v", err)
		}

		if daveBalResp.ConfirmedBalance <= davePreSweepBalance {
			predErr = fmt.Errorf("Dave should have more than %d "+
				"after sweep, instead has %d",
				davePreSweepBalance,
				daveBalResp.ConfirmedBalance)
			return false
		}

		return true
	}, defaultTimeout)
	if err != nil {
		t.Fatalf("%v", predErr)
	}

	// Dave should have no open channels.
	assertNodeNumChannels(t, dave, 0)
}
