package itest

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnrpc/invoicesrpc"
	"github.com/decred/dcrlnd/lnrpc/routerrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lntypes"
	"github.com/stretchr/testify/require"
)

// testOfflineHopInvoice tests whether trying to pay an invoice to an offline
// node fails as expected.
//
// This test creates the following network of channels:
//
//	Dave -> Carol    Alice -> Bob
//
// Then tries to perform a payment from Dave -> to Bob. This should fail, since
// there is no route connecting them. Carol and Alice are then connected,
// payments are performed. And a final test disconnecting Alice and trying to
// perform a new payment should also fail.
func testOfflineHopInvoice(net *lntest.NetworkHarness, t *harnessTest) {
	const chanAmt = dcrutil.Amount(100000)
	ctxb := context.Background()

	// Open a channel between Alice and Bob with Alice being the sole funder of
	// the channel.
	chanPointAlice := openChannelAndAssert(
		t, net, net.Alice, net.Bob,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// Create Dave's Node.
	dave := net.NewNode(t.t, "Dave", nil)
	defer shutdownAndAssert(net, t, dave)
	net.SendCoins(t.t, dcrutil.AtomsPerCoin, dave)

	carol := net.NewNode(t.t, "Carol", []string{"--nolisten"})
	defer shutdownAndAssert(net, t, carol)

	net.ConnectNodes(t.t, carol, dave)
	net.SendCoins(t.t, dcrutil.AtomsPerCoin, carol)

	chanPointDave := openChannelAndAssert(
		t, net, dave, carol,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// Generate 5 payment requests in Bob.
	const numPayments = 5
	const paymentAmt = 1000
	payReqs, _, _, err := createPayReqs(
		net.Bob, paymentAmt, numPayments,
	)
	if err != nil {
		t.Fatalf("unable to create pay reqs: %v", err)
	}

	// tryPayment tries to pay the given invoice from srcNode. It will check
	// if the returned error is the expected one.
	tryPayment := func(payReq string, srcNode *lntest.HarnessNode, expectedErr string) {
		sendReq := &lnrpc.SendRequest{
			PaymentRequest:       payReq,
			IgnoreMaxOutboundAmt: true,
		}
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		resp, err := srcNode.SendPaymentSync(ctxt, sendReq)
		if err != nil {
			t.Fatalf("unable to send payment: %v", err)
		}
		if resp.PaymentError != expectedErr {
			t.Fatalf("payment error (%v) != expected (%v)",
				resp.PaymentError, expectedErr)
		}
	}

	// Constants to make our lives easier.
	errNoPath := "unable to find a path to destination"
	success := ""

	// At this stage, our network looks like the following:
	//    Dave -> Carol    Alice -> Bob

	// Payment from Alice should work, given the Alice -> Bob link.
	tryPayment(payReqs[0], net.Alice, success)

	// Payments from Carol and Dave should _not_ work, given there is no route.
	tryPayment(payReqs[1], carol, errNoPath)
	tryPayment(payReqs[1], dave, errNoPath)

	// Connect Carol to Alice (but don't create a channel yet).
	net.EnsureConnected(t.t, carol, net.Alice)

	// Try to perform the payments from Carol and Dave. They should still fail.
	tryPayment(payReqs[1], carol, errNoPath)
	tryPayment(payReqs[1], dave, errNoPath)

	// Create a channel between Carol and Alice and wait for it to become valid.
	chanPointCarol := openChannelAndAssert(
		t, net, carol, net.Alice,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// Ensure Dave knows about the Carol -> Alice channel
	ctxt, _ := context.WithTimeout(context.Background(), defaultTimeout)
	if err = dave.WaitForNetworkChannelOpen(ctxt, chanPointCarol); err != nil {
		t.Fatalf("carol didn't advertise channel before "+
			"timeout: %v", err)
	}

	// TODO(decred): Fix this.
	//
	// This test fails after the upstream PR 2740 is merged due to network
	// partitions now taking discovery.DefaultHistoricalSyncInterval (10
	// minutes) to trigger a full graph re-discovery.
	//
	// This means dave doesn't get a gossip message describing the
	// Alice->Bob channel for a long time and fails to find a route to
	// perform the payments.
	//
	// We need some way of force triggering a historical graph sync in dave
	// after connecting carol and alice (or better yet some way of reliably
	// knowing that carol didn't previously relay that channel to him).
	time.Sleep(time.Second * 10)

	fundingTxId, _ := chainhash.NewHash(chanPointAlice.GetFundingTxidBytes())
	fmt.Printf("looking for %s\n", fundingTxId)

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp, err := dave.DescribeGraph(ctxt, &lnrpc.ChannelGraphRequest{})
	if err != nil {
		t.Fatalf("blergh: %v", err)
	}
	fmt.Println("Existing network graph")
	for _, e := range resp.Edges {
		fmt.Printf("edge %s\n   n1=%s\n   n2=%s\n", e.ChanPoint,
			e.Node1Pub, e.Node2Pub)
	}

	if err = dave.WaitForNetworkChannelOpen(ctxt, chanPointAlice); err != nil {
		t.Fatalf("dave didn't receive the alice->bob channel before "+
			"timeout: %v", err)
	}
	// At this stage, our network looks like the following:
	//    Dave -> Carol -> Alice -> Bob

	// Performing the payments should now work.
	tryPayment(payReqs[1], carol, success)
	tryPayment(payReqs[2], dave, success)

	// Disconnect Carol from Alice & Dave (simulating a broken link, carol
	// offline, etc)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if err := net.DisconnectNodes(ctxt, carol, net.Alice); err != nil {
		t.Fatalf("unable to disconnect carol from alice: %v", err)
	}
	if err := net.DisconnectNodes(ctxt, carol, dave); err != nil {
		t.Fatalf("unable to disconnect carol from dave: %v", err)
	}

	// Give some time for disconnection to finalize.
	time.Sleep(time.Second)

	// Starting payments from Carol and Dave should fail.
	tryPayment(payReqs[3], carol, errNoPath)
	tryPayment(payReqs[3], dave, errNoPath)

	// Reconnect Carol to Alice & Dave
	net.EnsureConnected(t.t, carol, net.Alice)
	net.EnsureConnected(t.t, carol, dave)

	// Give some time for reconnection to finalize.
	time.Sleep(time.Second)

	// Payments now succeed again.
	tryPayment(payReqs[3], carol, success)
	tryPayment(payReqs[4], dave, success)

	// Close the channels.
	closeChannelAndAssert(t, net, net.Alice, chanPointAlice, false)
	closeChannelAndAssert(t, net, carol, chanPointCarol, false)
	closeChannelAndAssert(t, net, dave, chanPointDave, false)
}

// testCalcPayStats asserts the correctness of the CalcPaymentStats RPC.
func testCalcPayStats(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb, cancel := context.WithCancel(context.Background())
	defer cancel()

	alice := net.NewNode(t.t, "Alice", nil)
	defer shutdownAndAssert(net, t, alice)
	net.SendCoins(t.t, dcrutil.AtomsPerCoin, alice)

	bob := net.NewNode(t.t, "Bob", nil)
	defer shutdownAndAssert(net, t, bob)
	net.ConnectNodes(t.t, alice, bob)

	// Open a channel between alice and bob.
	chanReq := lntest.OpenChannelParams{
		Amt: defaultChanAmt,
	}

	chanPoint := openChannelAndAssert(t, net, alice, bob, chanReq)

	// Complete 5 payments.
	const numPayments = 5
	const paymentAmt = 1000
	payReqs, _, _, err := createPayReqs(
		bob, paymentAmt, numPayments,
	)
	require.NoError(t.t, err)
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, alice, alice.RouterClient,
		payReqs, true,
	)
	require.NoError(t.t, err)

	// Create and fail a payment (by using a hold invoice).
	var (
		preimage = lntypes.Preimage{1, 2, 3, 4, 5}
		payHash  = preimage.Hash()
	)
	invoiceReq := &invoicesrpc.AddHoldInvoiceRequest{
		Value:      30000,
		CltvExpiry: 40,
		Hash:       payHash[:],
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	bobInvoice, err := bob.AddHoldInvoice(ctxt, invoiceReq)
	require.NoError(t.t, err)
	_, err = alice.RouterClient.SendPaymentV2(
		ctxb, &routerrpc.SendPaymentRequest{
			PaymentRequest: bobInvoice.PaymentRequest,
			TimeoutSeconds: 60,
			FeeLimitMAtoms: noFeeLimitMAtoms,
		},
	)
	require.NoError(t.t, err)
	waitForInvoiceAccepted(t, bob, payHash)
	_, err = bob.CancelInvoice(ctxt, &invoicesrpc.CancelInvoiceMsg{PaymentHash: payHash[:]})
	require.NoError(t.t, err)

	// Create but do not settle a payment (by using a hold invoice).
	var (
		preimage2 = lntypes.Preimage{1, 2, 3, 4, 5, 6}
		payHash2  = preimage2.Hash()
	)
	invoiceReq2 := &invoicesrpc.AddHoldInvoiceRequest{
		Value:      30000,
		CltvExpiry: 40,
		Hash:       payHash2[:],
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	bobInvoice2, err := bob.AddHoldInvoice(ctxt, invoiceReq2)
	require.NoError(t.t, err)
	_, err = alice.RouterClient.SendPaymentV2(
		ctxb, &routerrpc.SendPaymentRequest{
			PaymentRequest: bobInvoice2.PaymentRequest,
			TimeoutSeconds: 60,
			FeeLimitMAtoms: noFeeLimitMAtoms,
		},
	)
	require.NoError(t.t, err)
	waitForInvoiceAccepted(t, bob, payHash2)

	// Fetch the payment stats.
	payStats, err := alice.CalcPaymentStats(testctx.WithTimeout(t, defaultTimeout), &lnrpc.CalcPaymentStatsRequest{})
	require.NoError(t.t, err)
	require.Equal(t.t, uint64(7), payStats.Total)
	require.Equal(t.t, uint64(5), payStats.Succeeded)
	require.Equal(t.t, uint64(1), payStats.Failed)
	require.Equal(t.t, uint64(7), payStats.HtlcAttempts)
	require.Equal(t.t, uint64(1), payStats.HtlcFailed)
	require.Equal(t.t, uint64(5), payStats.HtlcSettled)
	require.Equal(t.t, uint64(0), payStats.OldDupePayments)

	// Clean up the channel.
	_, err = bob.CancelInvoice(testctx.New(t), &invoicesrpc.CancelInvoiceMsg{PaymentHash: payHash2[:]})
	require.NoError(t.t, err)
	closeChannelAndAssert(t, net, alice, chanPoint, false)
}
