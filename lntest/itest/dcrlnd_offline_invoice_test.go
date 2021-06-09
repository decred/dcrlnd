package itest

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
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
	ctxt, _ := context.WithTimeout(ctxb, channelOpenTimeout)
	chanPointAlice := openChannelAndAssert(
		ctxt, t, net, net.Alice, net.Bob,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// Create Dave's Node.
	dave := net.NewNode(t.t, "Dave", nil)
	defer shutdownAndAssert(net, t, dave)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, dave)

	carol := net.NewNode(t.t, "Carol", []string{"--nolisten"})
	defer shutdownAndAssert(net, t, carol)

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if err := net.ConnectNodes(ctxt, carol, dave); err != nil {
		t.Fatalf("unable to connect carol to dave: %v", err)
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	chanPointDave := openChannelAndAssert(
		ctxt, t, net, dave, carol,
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
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if err := net.EnsureConnected(ctxt, carol, net.Alice); err != nil {
		t.Fatalf("unable to connect carol to Alice %v",
			err)
	}

	// Try to perform the payments from Carol and Dave. They should still fail.
	tryPayment(payReqs[1], carol, errNoPath)
	tryPayment(payReqs[1], dave, errNoPath)

	// Create a channel between Carol and Alice and wait for it to become valid.
	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	chanPointCarol := openChannelAndAssert(
		ctxt, t, net, carol, net.Alice,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// Ensure Dave knows about the Carol -> Alice channel
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
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if err := net.EnsureConnected(ctxt, carol, net.Alice); err != nil {
		t.Fatalf("unable to connect carol to Alice %v",
			err)
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if err := net.EnsureConnected(ctxt, carol, dave); err != nil {
		t.Fatalf("unable to connect carol to Alice %v",
			err)
	}

	// Give some time for reconnection to finalize.
	time.Sleep(time.Second)

	// Payments now succeed again.
	tryPayment(payReqs[3], carol, success)
	tryPayment(payReqs[4], dave, success)

	// Close the channels.
	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	closeChannelAndAssert(ctxt, t, net, net.Alice, chanPointAlice, false)
	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	closeChannelAndAssert(ctxt, t, net, carol, chanPointCarol, false)
	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	closeChannelAndAssert(ctxt, t, net, dave, chanPointDave, false)
}
