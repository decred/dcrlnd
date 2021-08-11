package itest

import (
	"context"
	"errors"
	"strings"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
)

// testAddInvoiceMaxInboundAmt tests whether trying to add an invoice fails
// under various circumstances when taking into account the maximum inbound
// amount available in directly connected channels.
func testAddInvoiceMaxInboundAmt(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	// Create a Carol node to use in tests. All invoices will be created on
	// her node.
	carol := net.NewNode(t.t, "Carol", []string{"--nolisten"})
	defer shutdownAndAssert(net, t, carol)

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.ConnectNodes(ctxt, t.t, carol, net.Bob)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	// Closure to help on tests.
	addInvoice := func(value int64, ignoreMaxInbound bool) error {
		invoice := &lnrpc.Invoice{
			Value:               value,
			IgnoreMaxInboundAmt: ignoreMaxInbound,
		}
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		_, err := carol.AddInvoice(ctxt, invoice)
		return err
	}

	// Test that adding an invoice when Carol doesn't have any open channels
	// fails.
	err := addInvoice(0, false)
	if err == nil {
		t.Fatalf("adding invoice without open channels should return an error")
	}
	if !strings.Contains(err.Error(), "no open channels") {
		t.Fatalf("adding invoice without open channels should return " +
			"correct error")
	}

	// Same test, but ignoring inbound amounts succeeds.
	err = addInvoice(0, true)
	if err != nil {
		t.Fatalf("adding invoice ignoring inbound should succeed without open " +
			"channels")
	}

	// Now open a channel from Carol -> Bob.
	chanAmt := int64(1000000)
	pushAmt := chanAmt / 2
	chanReserve := chanAmt / 100
	channelParam := lntest.OpenChannelParams{
		Amt:     dcrutil.Amount(chanAmt),
		PushAmt: dcrutil.Amount(pushAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	chanPoint := openChannelAndAssert(
		ctxt, t, net, carol, net.Bob, channelParam)

	// Test various scenarios with the channel open. The maximum amount receivable
	// from a channel should be the remote channel balance minus our required
	// channel reserve to it.
	testCasesWithChan := []struct {
		value         int64
		ignoreInbound bool
		valid         bool
		descr         string
	}{
		// Test accounting for max inbound amount.
		{0, false, true, "zero amount"},
		{1, false, true, "one amount"},
		{pushAmt - chanReserve, false, true, "maximum amount"},
		{pushAmt - chanReserve + 1, false, false, "maximum amount +1"},
		{pushAmt, false, false, "total push amount"},
		{chanAmt, false, false, "total chan amount"},

		// Test ignoring max inbound amount.
		{pushAmt - chanReserve + 1, true, true, "maximum amount +1"},
		{pushAmt, true, true, "total push amount"},
		{chanAmt, true, true, "total chan amount"},
	}

	for _, tc := range testCasesWithChan {
		err := addInvoice(tc.value, tc.ignoreInbound)
		if tc.valid && err != nil {
			t.Fatalf("case %s with ignore %v returned error '%v' but should "+
				"have returned nil", tc.descr, tc.ignoreInbound, err)
		}
		if !tc.valid && err == nil {
			t.Fatalf("case %s with ignore %v returned valid but should "+
				"have returned error", tc.descr, tc.ignoreInbound)
		}
		if !tc.valid && !strings.Contains(err.Error(), "not enough inbound capacity") {
			t.Fatalf("case %s with ignore %v did not return expected "+
				"'not enough capacity' error (returned '%v')", tc.descr,
				tc.ignoreInbound, err)
		}
	}

	peerReq := &lnrpc.PeerEventSubscription{}
	carolPeerClient, err := carol.SubscribePeerEvents(ctxb, peerReq)
	require.NoError(t.t, err)

	// Disconnect the nodes from one another. While their channel remains open,
	// carol cannot receive payments (since bob is offline from her POV).
	err = net.DisconnectNodes(ctxb, carol, net.Bob)
	if err != nil {
		t.Fatalf("unable to disconnect carol and bob: %v", err)
	}

	// Wait to receive the PEER_OFFLINE event before trying to create the
	// invoice.
	peerEvent, err := carolPeerClient.Recv()
	require.NoError(t.t, err)
	require.Equal(t.t, lnrpc.PeerEvent_PEER_OFFLINE, peerEvent.GetType())

	// Now trying to add an invoice with an offline peer should fail.
	err = addInvoice(0, false)
	if err == nil {
		t.Fatalf("adding invoice without online peer should return an error")
	}
	if !strings.Contains(err.Error(), "no online channels found") {
		t.Fatalf("adding invoice without online channels should return " +
			"correct error")
	}

	// But adding an invoice ignoring inbound capacity should still work
	err = addInvoice(0, true)
	if err != nil {
		t.Fatalf("adding invoice ignoring inbound should succeed without open " +
			"channels")
	}

	// Force-close and cleanup the channel, since bob & carol are disconnected.
	closeChannelAndAssert(t, net, carol, chanPoint, true)
	cleanupForceClose(t, net, carol, chanPoint)
}

// testAddReceiveInvoiceMaxInboundAmt tests whether, after verifying that an
// invoice can be added _without_ ignoring the maximum inbound capacity, that
// same invoice can actually be paid by another node.
//
// In particular, it tests invoices at the limit of draining the channel.
func testAddReceiveInvoiceMaxInboundAmt(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	// Create and fund a Carol node to use in tests. All invoices will be
	// created on her node.
	carol := net.NewNode(t.t, "Carol", nil)
	defer shutdownAndAssert(net, t, carol)

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.ConnectNodes(ctxt, t.t, carol, net.Bob)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	// Now open a channel from Carol -> Bob.
	chanAmt := int64(1000000)
	pushAmt := chanAmt / 2
	chanReserve := chanAmt / 100
	channelParam := lntest.OpenChannelParams{
		Amt:     dcrutil.Amount(chanAmt),
		PushAmt: dcrutil.Amount(pushAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	chanPointCarol := openChannelAndAssert(
		ctxt, t, net, carol, net.Bob, channelParam)

	// Also open a channel from Alice -> bob. Alice will attempt the payments.
	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	channelParam = lntest.OpenChannelParams{
		Amt: dcrutil.Amount(chanAmt),
	}
	chanPointAlice := openChannelAndAssert(
		ctxt, t, net, net.Alice, net.Bob, channelParam)

	// Sanity check that payments one atom larger than the channel capacity -
	// reserve cannot be paid.
	maxInboundCap := pushAmt - chanReserve
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	_, err := carol.AddInvoice(ctxt, &lnrpc.Invoice{Value: maxInboundCap + 1})
	if err == nil {
		t.Fatalf("adding an invoice for maxInboundCap + 1 should fail")
	}

	// Generate a valid invoice with the maximum amount possible.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	invoice, err := carol.AddInvoice(ctxt, &lnrpc.Invoice{Value: maxInboundCap})
	if err != nil {
		t.Fatalf("unable to add invoice at maxInboundCap: %v", err)
	}

	// Try and pay this invoice from Alice. It should succeed.
	sendReq := &lnrpc.SendRequest{PaymentRequest: invoice.PaymentRequest}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp, err := net.Alice.SendPaymentSync(ctxt, sendReq)
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}
	if resp.PaymentError != "" {
		t.Fatalf("error when attempting recv: %v", resp.PaymentError)
	}

	// Verify the inbound channel balance is now 0.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	balances, err := carol.ChannelBalance(ctxt, &lnrpc.ChannelBalanceRequest{})
	if err != nil {
		t.Fatalf("unable to obtain balances: %v", err)
	}
	if balances.MaxInboundAmount != 0 {
		t.Fatalf("max inbound amount not drained (%d remaining)",
			balances.MaxInboundAmount)
	}

	// Try to generate a new invoice with value of one atom. It should fail,
	// since we have now drained the inbound capacity of Carol.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	_, err = carol.AddInvoice(ctxt, &lnrpc.Invoice{Value: 1})
	if err == nil {
		t.Fatalf("invoice sould not be generated after draining " +
			"inbound capacity")
	}

	closeChannelAndAssert(t, net, carol, chanPointCarol, false)
	closeChannelAndAssert(t, net, net.Alice, chanPointAlice, false)
}

// testSendPaymentMaxAmt tests whether trying to send an invoice fails under
// various circumstances when taking into account the maximum outbound amount
// available in directly connected channels.
func testSendPaymentMaxOutboundAmt(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	// Create a Carol node to use in tests. All invoices will be created on
	// her node.
	carol := net.NewNode(t.t, "Carol", []string{"--nolisten"})
	defer shutdownAndAssert(net, t, carol)

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.ConnectNodes(ctxt, t.t, carol, net.Bob)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	// Create an invoice with zero amount on Bob for the next tests.
	invoice, err := net.Bob.AddInvoice(ctxt, &lnrpc.Invoice{IgnoreMaxInboundAmt: true})
	if err != nil {
		t.Fatalf("unable to create invoice in Bob: %v", err)
	}

	// Closure to help on tests.
	sendPayment := func(value int64) error {
		// Dummy send request to Bob node that fails due to wrong
		// payment hash.
		sendReq := &lnrpc.SendRequest{
			PaymentRequest: invoice.PaymentRequest,
			Amt:            value,
		}
		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		resp, err := carol.SendPaymentSync(ctxt, sendReq)
		if err != nil {
			return err
		}
		if resp.PaymentError != "" {
			return errors.New(resp.PaymentError)
		}
		return nil
	}

	// Test that adding an invoice when Carol doesn't have any open channels
	// fails.
	err = sendPayment(1)
	if err == nil {
		t.Fatalf("sending payment without open channels should return an error")
	}
	if !strings.Contains(err.Error(), "no open channels") {
		t.Fatalf("sending payment without open channels should return " +
			"correct error")
	}

	// Now open a channel from Carol -> Bob.
	ctype := commitTypeLegacy
	chanAmt := int64(1000000)
	pushAmt := chanAmt / 2
	chanReserve := chanAmt / 100
	txFee := ctype.calcStaticFee(0)
	peerHtlcFee := ctype.calcStaticFee(1) - ctype.calcStaticFee(0)
	localAmt := chanAmt - pushAmt - int64(txFee)
	maxPayAmt := localAmt - chanReserve
	channelParam := lntest.OpenChannelParams{
		Amt:     dcrutil.Amount(chanAmt),
		PushAmt: dcrutil.Amount(pushAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, channelOpenTimeout)
	chanPoint := openChannelAndAssert(
		ctxt, t, net, carol, net.Bob, channelParam)

	// Try to send a payment for one atom more than the maximum possible
	// amount.  It should fail due to not enough outbound capacity.
	err = sendPayment(maxPayAmt + 1)
	if err == nil {
		t.Fatalf("payment higher than max amount should fail, but suceeded.")
	}
	if !strings.Contains(err.Error(), "not enough outbound capacity") {
		t.Fatalf("payment failed for the wrong reason: %v", err)
	}

	// Try to send a payment for exactly the maximum possible amount. It
	// should succeed.
	err = sendPayment(maxPayAmt - int64(peerHtlcFee))
	if err != nil {
		t.Fatalf("payment should have suceeded, but failed with %v", err)
	}

	// Wait until Bob and Carol show no pending HTLCs before proceding.
	for _, node := range []*lntest.HarnessNode{net.Bob, carol} {
		err := waitForPendingHtlcs(node, chanPoint, 0)
		if err != nil {
			t.Fatalf("node %s still showing pending HTLCs: %v",
				node.Name(), err)
		}
	}

	peerReq := &lnrpc.PeerEventSubscription{}
	carolPeerClient, err := carol.SubscribePeerEvents(ctxb, peerReq)
	require.NoError(t.t, err)

	// Disconnect the nodes from one another. While their channel remains
	// open, carol cannot send payments (since bob is offline from her
	// POV).
	err = net.DisconnectNodes(ctxb, carol, net.Bob)
	if err != nil {
		t.Fatalf("unable to disconnect carol and bob: %v", err)
	}

	// Wait to receive the PEER_OFFLINE event before trying to create the
	// invoice.
	peerEvent, err := carolPeerClient.Recv()
	require.NoError(t.t, err)
	require.Equal(t.t, lnrpc.PeerEvent_PEER_OFFLINE, peerEvent.GetType())

	// Now trying to send a payment with an offline peer should fail.
	err = sendPayment(1)
	if err == nil {
		t.Fatalf("sending payment without online peer should return an error")
	}
	if !strings.Contains(err.Error(), "no online channels found") {
		t.Fatalf("sending payment without online channels should return " +
			"correct error")
	}

	// Stop Carol to perform the forced channel close.
	if err := net.StopNode(carol); err != nil {
		t.Fatalf("unable to stop Carol: %v", err)
	}

	// Force-close and cleanup the channel, since bob & carol are
	// disconnected.
	closeChannelAndAssert(t, net, net.Bob, chanPoint, true)
	cleanupForceClose(t, net, net.Bob, chanPoint)
}

// testMaxIOChannelBalances tests whether the max inbound/outbound amounts for
// channel balances are consistent.
func testMaxIOChannelBalances(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	// Create a new Carol node to ensure no older channels interfere with
	// the test. Connect Carol to Bob and fund her.
	carol := net.NewNode(t.t, "Carol", nil)
	defer shutdownAndAssert(net, t, carol)

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	net.ConnectNodes(ctxt, t.t, carol, net.Bob)
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	// Closures to help with tests.

	openChan := func(chanAmt, pushAmt int64, reverse bool) *lnrpc.ChannelPoint {
		channelParam := lntest.OpenChannelParams{
			Amt:     dcrutil.Amount(chanAmt),
			PushAmt: dcrutil.Amount(pushAmt),
		}
		ctxt, _ := context.WithTimeout(ctxb, channelOpenTimeout)
		if reverse {
			return openChannelAndAssert(
				ctxt, t, net, net.Bob, carol, channelParam,
			)
		}
		return openChannelAndAssert(
			ctxt, t, net, carol, net.Bob, channelParam,
		)
	}

	assertBalances := func(balance, maxInbound, maxOutbound, otherBalance,
		otherMaxInbound, otherMaxOutbound int64) {

		ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
		b, err := carol.ChannelBalance(ctxt, &lnrpc.ChannelBalanceRequest{})
		if err != nil {
			t.t.Errorf("unable to read channel balance: %v", err)
			return
		}
		if b.Balance != balance {
			t.t.Errorf("balance mismatch; expected=%d actual=%d ",
				balance, b.Balance)
		}
		if b.MaxInboundAmount != maxInbound {
			t.t.Errorf("maxInbound mismatch: expected=%d actual=%d",
				maxInbound, b.MaxInboundAmount)
		}
		if b.MaxOutboundAmount != maxOutbound {
			t.t.Errorf("maxOutbound mismatch: expected=%d, actual=%d",
				maxOutbound, b.MaxOutboundAmount)
		}

		// Now check the balance from bob's pov
		ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
		b, err = net.Bob.ChannelBalance(ctxt, &lnrpc.ChannelBalanceRequest{})
		if err != nil {
			t.t.Errorf("unable to read bob channel balance: %v", err)
			return
		}
		if b.Balance != otherBalance {
			t.t.Errorf("otherBalance mismatch; expected=%d actual=%d ",
				otherBalance, b.Balance)
		}
		if b.MaxInboundAmount != otherMaxInbound {
			t.t.Errorf("otherMaxInbound mismatch: expected=%d actual=%d",
				otherMaxInbound, b.MaxInboundAmount)
		}
		if b.MaxOutboundAmount != otherMaxOutbound {
			t.t.Errorf("otherMaxOutbound mismatch: expected=%d, actual=%d",
				otherMaxOutbound, b.MaxOutboundAmount)
		}
	}

	chanAmt := int64(1000000)
	ctype := commitTypeLegacy
	txFee := int64(ctype.calcStaticFee(0))
	chanReserve := chanAmt / 100
	dustLimit := int64(6030) // dust limit at the network relay fee rate

	// Define the test scenarios. All of them use the same total channel
	// amount (chanAmt) and reserve. For each test, a new channel will be
	// opened, the balance will be verified, then the channel will be
	// closed.
	testCases := []struct {
		pushAmt          int64
		balance          int64
		maxInbound       int64
		maxOutbound      int64
		otherBalance     int64
		otherMaxInbound  int64
		otherMaxOutbound int64
		reverse          bool
		descr            string
	}{
		{
			balance:         chanAmt - txFee,
			maxOutbound:     chanAmt - txFee - chanReserve,
			otherMaxInbound: chanAmt - txFee - chanReserve,
			descr:           "all funds remain in Carol",
		},
		{
			pushAmt:         chanReserve / 2,
			balance:         chanAmt - txFee - chanReserve/2,
			maxOutbound:     chanAmt - txFee - chanReserve/2 - chanReserve,
			otherBalance:    chanReserve / 2,
			otherMaxInbound: chanAmt - txFee - chanReserve - chanReserve/2,
			descr:           "carol sent half the remote reserve amount",
		},
		{
			pushAmt:         chanReserve,
			balance:         chanAmt - txFee - chanReserve,
			maxOutbound:     chanAmt - txFee - chanReserve - chanReserve,
			otherBalance:    chanReserve,
			otherMaxInbound: chanAmt - txFee - chanReserve - chanReserve,
			descr:           "carol sent exactly the remote chan reserve",
		},
		{
			pushAmt:          chanReserve + 1,
			balance:          chanAmt - txFee - chanReserve - 1,
			maxOutbound:      chanAmt - txFee - chanReserve - chanReserve - 1,
			maxInbound:       1,
			otherBalance:     chanReserve + 1,
			otherMaxOutbound: 1,
			otherMaxInbound:  chanAmt - txFee - chanReserve - chanReserve - 1,
			descr:            "carol sent one more than the remote chan reserve",
		},
		{
			pushAmt:          chanReserve + 20000,
			balance:          chanAmt - txFee - chanReserve - 20000,
			maxOutbound:      chanAmt - txFee - chanReserve - chanReserve - 20000,
			maxInbound:       20000,
			otherBalance:     chanReserve + 20000,
			otherMaxOutbound: 20000,
			otherMaxInbound:  chanAmt - txFee - chanReserve - chanReserve - 20000,
			descr:            "carol sent 20k atoms over the chan reserve",
		},
		{
			pushAmt:          chanAmt - txFee - chanReserve - 2*dustLimit - 1,
			balance:          chanReserve + 2*dustLimit + 1,
			maxOutbound:      2*dustLimit + 1,
			maxInbound:       chanAmt - txFee - chanReserve*2 - 2*dustLimit - 1,
			otherBalance:     chanAmt - txFee - chanReserve - 2*dustLimit - 1,
			otherMaxOutbound: chanAmt - txFee - chanReserve*2 - 2*dustLimit - 1,
			otherMaxInbound:  2*dustLimit + 1,
			descr:            "carol sent one less than the maximum pushable during funding",
		},
		{
			pushAmt:          chanAmt - txFee - chanReserve - 2*dustLimit,
			balance:          chanReserve + 2*dustLimit,
			maxOutbound:      2 * dustLimit,
			maxInbound:       chanAmt - txFee - chanReserve*2 - 2*dustLimit,
			otherBalance:     chanAmt - txFee - chanReserve - 2*dustLimit,
			otherMaxOutbound: chanAmt - txFee - chanReserve*2 - 2*dustLimit,
			otherMaxInbound:  2 * dustLimit,
			descr:            "carol sent exactly the maximum pushable during funding",
		},
	}

	for _, tc := range testCases {
		// Sanity check before opening the channel that all balances
		// are zero.
		assertBalances(0, 0, 0, 0, 0, 0)
		if t.t.Failed() {
			t.Fatalf("case '%s' returned error in zero sanity check",
				tc.descr)
		}

		// Open the channel, perform the balance check, then close the
		// channel again.
		chanPoint := openChan(chanAmt, tc.pushAmt, tc.reverse)
		assertBalances(
			tc.balance, tc.maxInbound, tc.maxOutbound,
			tc.otherBalance, tc.otherMaxInbound,
			tc.otherMaxOutbound,
		)
		if t.t.Failed() {
			t.Fatalf("Case '%s' failed", tc.descr)
		}
		closeChannelAndAssert(t, net, carol, chanPoint, false)
	}
}
