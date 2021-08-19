package itest

import (
	"context"
	"strings"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/chainreg"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/decred/dcrlnd/lnwire"
)

// testUpdateChannelPolicy tests that policy updates made to a channel
// gets propagated to other nodes in the network.
func testUpdateChannelPolicy(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	const (
		defaultFeeBase       = 1000
		defaultFeeRate       = 1
		defaultTimeLockDelta = chainreg.DefaultDecredTimeLockDelta
		defaultMinHtlc       = 1000
	)

	// Launch notification clients for all nodes, such that we can
	// get notified when they discover new channels and updates in the
	// graph.
	aliceSub := subscribeGraphNotifications(ctxb, t, net.Alice)
	defer close(aliceSub.quit)
	bobSub := subscribeGraphNotifications(ctxb, t, net.Bob)
	defer close(bobSub.quit)

	chanAmt := defaultChanAmt
	pushAmt := chanAmt / 2
	defaultMaxHtlc := calculateMaxHtlc(chanAmt)

	// Create a channel Alice->Bob.
	chanPoint := openChannelAndAssert(
		t, net, net.Alice, net.Bob,
		lntest.OpenChannelParams{
			Amt:     chanAmt,
			PushAmt: pushAmt,
		},
	)

	// We add all the nodes' update channels to a slice, such that we can
	// make sure they all receive the expected updates.
	graphSubs := []graphSubscription{aliceSub, bobSub}
	nodes := []*lntest.HarnessNode{net.Alice, net.Bob}

	// Alice and Bob should see each other's ChannelUpdates, advertising the
	// default routing policies.
	expectedPolicy := &lnrpc.RoutingPolicy{
		FeeBaseMAtoms:      defaultFeeBase,
		FeeRateMilliMAtoms: defaultFeeRate,
		TimeLockDelta:      defaultTimeLockDelta,
		MinHtlc:            defaultMinHtlc,
		MaxHtlcMAtoms:      defaultMaxHtlc,
	}

	for _, graphSub := range graphSubs {
		waitForChannelUpdate(
			t, graphSub,
			[]expectedChanUpdate{
				{net.Alice.PubKeyStr, expectedPolicy, chanPoint},
				{net.Bob.PubKeyStr, expectedPolicy, chanPoint},
			},
		)
	}

	// They should now know about the default policies.
	for _, node := range nodes {
		assertChannelPolicy(
			t, node, net.Alice.PubKeyStr, expectedPolicy, chanPoint,
		)
		assertChannelPolicy(
			t, node, net.Bob.PubKeyStr, expectedPolicy, chanPoint,
		)
	}

	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	err := net.Alice.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("alice didn't report channel: %v", err)
	}
	err = net.Bob.WaitForNetworkChannelOpen(ctxt, chanPoint)
	if err != nil {
		t.Fatalf("bob didn't report channel: %v", err)
	}

	// Create Carol with options to rate limit channel updates up to 2 per
	// day, and create a new channel Bob->Carol.
	carol := net.NewNode(
		t.t, "Carol", []string{
			"--gossip.max-channel-update-burst=2",
			"--gossip.channel-update-interval=24h",
		},
	)

	// Clean up carol's node when the test finishes.
	defer shutdownAndAssert(net, t, carol)

	carolSub := subscribeGraphNotifications(ctxb, t, carol)
	defer close(carolSub.quit)

	graphSubs = append(graphSubs, carolSub)
	nodes = append(nodes, carol)

	// Send some coins to Carol that can be used for channel funding.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	net.SendCoins(ctxt, t.t, dcrutil.AtomsPerCoin, carol)

	net.ConnectNodes(t.t, carol, net.Bob)

	// Open the channel Carol->Bob with a custom min_htlc value set. Since
	// Carol is opening the channel, she will require Bob to not forward
	// HTLCs smaller than this value, and hence he should advertise it as
	// part of his ChannelUpdate.
	const customMinHtlc = 5000
	chanPoint2 := openChannelAndAssert(
		t, net, carol, net.Bob,
		lntest.OpenChannelParams{
			Amt:     chanAmt,
			PushAmt: pushAmt,
			MinHtlc: customMinHtlc,
		},
	)

	expectedPolicyBob := &lnrpc.RoutingPolicy{
		FeeBaseMAtoms:      defaultFeeBase,
		FeeRateMilliMAtoms: defaultFeeRate,
		TimeLockDelta:      defaultTimeLockDelta,
		MinHtlc:            customMinHtlc,
		MaxHtlcMAtoms:      defaultMaxHtlc,
	}
	expectedPolicyCarol := &lnrpc.RoutingPolicy{
		FeeBaseMAtoms:      defaultFeeBase,
		FeeRateMilliMAtoms: defaultFeeRate,
		TimeLockDelta:      defaultTimeLockDelta,
		MinHtlc:            defaultMinHtlc,
		MaxHtlcMAtoms:      defaultMaxHtlc,
	}

	for _, graphSub := range graphSubs {
		waitForChannelUpdate(
			t, graphSub,
			[]expectedChanUpdate{
				{net.Bob.PubKeyStr, expectedPolicyBob, chanPoint2},
				{carol.PubKeyStr, expectedPolicyCarol, chanPoint2},
			},
		)
	}

	// Check that all nodes now know about the updated policies.
	for _, node := range nodes {
		assertChannelPolicy(
			t, node, net.Bob.PubKeyStr, expectedPolicyBob,
			chanPoint2,
		)
		assertChannelPolicy(
			t, node, carol.PubKeyStr, expectedPolicyCarol,
			chanPoint2,
		)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.Alice.WaitForNetworkChannelOpen(ctxt, chanPoint2)
	if err != nil {
		t.Fatalf("alice didn't report channel: %v", err)
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.Bob.WaitForNetworkChannelOpen(ctxt, chanPoint2)
	if err != nil {
		t.Fatalf("bob didn't report channel: %v", err)
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = carol.WaitForNetworkChannelOpen(ctxt, chanPoint2)
	if err != nil {
		t.Fatalf("carol didn't report channel: %v", err)
	}

	// First we'll try to send a payment from Alice to Carol with an amount
	// less than the min_htlc value required by Carol. This payment should
	// fail, as the channel Bob->Carol cannot carry HTLCs this small.
	payAmt := dcrutil.Amount(4)
	invoice := &lnrpc.Invoice{
		Memo:  "testing",
		Value: int64(payAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp, err := carol.AddInvoice(ctxt, invoice)
	if err != nil {
		t.Fatalf("unable to add invoice: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, net.Alice, net.Alice.RouterClient,
		[]string{resp.PaymentRequest}, true,
	)

	// Alice knows about the channel policy of Carol and should therefore
	// not be able to find a path during routing.
	expErr := lnrpc.PaymentFailureReason_FAILURE_REASON_NO_ROUTE
	if err.Error() != expErr.String() {
		t.Fatalf("expected %v, instead got %v", expErr, err)
	}

	// Now we try to send a payment over the channel with a value too low
	// to be accepted. First we query for a route to route a payment of
	// 5000 milli-atoms, as this is accepted.
	payAmt = dcrutil.Amount(5)
	routesReq := &lnrpc.QueryRoutesRequest{
		PubKey:         carol.PubKeyStr,
		Amt:            int64(payAmt),
		FinalCltvDelta: defaultTimeLockDelta,
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	routes, err := net.Alice.QueryRoutes(ctxt, routesReq)
	if err != nil {
		t.Fatalf("unable to get route: %v", err)
	}

	if len(routes.Routes) != 1 {
		t.Fatalf("expected to find 1 route, got %v", len(routes.Routes))
	}

	// We change the route to carry a payment of 4000 milli-atoms instead of 5000
	// milli-atoms.
	payAmt = dcrutil.Amount(4)
	amtAtoms := int64(payAmt)
	amtMAtoms := int64(lnwire.NewMAtomsFromAtoms(payAmt))
	routes.Routes[0].Hops[0].AmtToForward = amtAtoms // nolint:staticcheck
	routes.Routes[0].Hops[0].AmtToForwardMAtoms = amtMAtoms
	routes.Routes[0].Hops[1].AmtToForward = amtAtoms // nolint:staticcheck
	routes.Routes[0].Hops[1].AmtToForwardMAtoms = amtMAtoms

	// Send the payment with the modified value.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	alicePayStream, err := net.Alice.SendToRoute(ctxt) // nolint:staticcheck
	if err != nil {
		t.Fatalf("unable to create payment stream for alice: %v", err)
	}
	sendReq := &lnrpc.SendToRouteRequest{
		PaymentHash: resp.RHash,
		Route:       routes.Routes[0],
	}

	err = alicePayStream.Send(sendReq)
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}

	// We expect this payment to fail, and that the min_htlc value is
	// communicated back to us, since the attempted HTLC value was too low.
	sendResp, err := alicePayStream.Recv()
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}

	// Expected as part of the error message.
	substrs := []string{
		"AmountBelowMinimum",
		"HtlcMinimumMAtoms: (lnwire.MilliAtom) 5000 milli-atoms",
	}
	for _, s := range substrs {
		if !strings.Contains(sendResp.PaymentError, s) {
			t.Fatalf("expected error to contain \"%v\", instead "+
				"got %v", s, sendResp.PaymentError)
		}
	}

	// Make sure sending using the original value succeeds.
	payAmt = dcrutil.Amount(5)
	amtAtoms = int64(payAmt)
	amtMAtoms = int64(lnwire.NewMAtomsFromAtoms(payAmt))
	routes.Routes[0].Hops[0].AmtToForward = amtAtoms // nolint:staticcheck
	routes.Routes[0].Hops[0].AmtToForwardMAtoms = amtMAtoms
	routes.Routes[0].Hops[1].AmtToForward = amtAtoms // nolint:staticcheck
	routes.Routes[0].Hops[1].AmtToForwardMAtoms = amtMAtoms

	// Manually set the MPP payload a new for each payment since
	// the payment addr will change with each invoice, although we
	// can re-use the route itself.
	route := routes.Routes[0]
	route.Hops[len(route.Hops)-1].TlvPayload = true
	route.Hops[len(route.Hops)-1].MppRecord = &lnrpc.MPPRecord{
		PaymentAddr:    resp.PaymentAddr,
		TotalAmtMAtoms: amtMAtoms,
	}

	sendReq = &lnrpc.SendToRouteRequest{
		PaymentHash: resp.RHash,
		Route:       route,
	}

	err = alicePayStream.Send(sendReq)
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}

	sendResp, err = alicePayStream.Recv()
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}

	if sendResp.PaymentError != "" {
		t.Fatalf("expected payment to succeed, instead got %v",
			sendResp.PaymentError)
	}

	// With our little cluster set up, we'll update the fees and the max htlc
	// size for the Bob side of the Alice->Bob channel, and make sure
	// all nodes learn about it.
	baseFee := int64(1500)
	feeRate := int64(12)
	timeLockDelta := uint32(66)
	maxHtlc := uint64(500000)

	expectedPolicy = &lnrpc.RoutingPolicy{
		FeeBaseMAtoms:      baseFee,
		FeeRateMilliMAtoms: testFeeBase * feeRate,
		TimeLockDelta:      timeLockDelta,
		MinHtlc:            defaultMinHtlc,
		MaxHtlcMAtoms:      maxHtlc,
	}

	req := &lnrpc.PolicyUpdateRequest{
		BaseFeeMAtoms: baseFee,
		FeeRate:       float64(feeRate),
		TimeLockDelta: timeLockDelta,
		MaxHtlcMAtoms: maxHtlc,
		Scope: &lnrpc.PolicyUpdateRequest_ChanPoint{
			ChanPoint: chanPoint,
		},
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if _, err := net.Bob.UpdateChannelPolicy(ctxt, req); err != nil {
		t.Fatalf("unable to get alice's balance: %v", err)
	}

	// Wait for all nodes to have seen the policy update done by Bob.
	for _, graphSub := range graphSubs {
		waitForChannelUpdate(
			t, graphSub,
			[]expectedChanUpdate{
				{net.Bob.PubKeyStr, expectedPolicy, chanPoint},
			},
		)
	}

	// Check that all nodes now know about Bob's updated policy.
	for _, node := range nodes {
		assertChannelPolicy(
			t, node, net.Bob.PubKeyStr, expectedPolicy, chanPoint,
		)
	}

	// Now that all nodes have received the new channel update, we'll try
	// to send a payment from Alice to Carol to ensure that Alice has
	// internalized this fee update. This shouldn't affect the route that
	// Alice takes though: we updated the Alice -> Bob channel and she
	// doesn't pay for transit over that channel as it's direct.
	// Note that the payment amount is >= the min_htlc value for the
	// channel Bob->Carol, so it should successfully be forwarded.
	payAmt = dcrutil.Amount(5)
	invoice = &lnrpc.Invoice{
		Memo:  "testing",
		Value: int64(payAmt),
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp, err = carol.AddInvoice(ctxt, invoice)
	if err != nil {
		t.Fatalf("unable to add invoice: %v", err)
	}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = completePaymentRequests(
		ctxt, net.Alice, net.Alice.RouterClient,
		[]string{resp.PaymentRequest}, true,
	)
	if err != nil {
		t.Fatalf("unable to send payment: %v", err)
	}

	// We'll now open a channel from Alice directly to Carol.
	net.ConnectNodes(t.t, net.Alice, carol)
	chanPoint3 := openChannelAndAssert(
		t, net, net.Alice, carol,
		lntest.OpenChannelParams{
			Amt:     chanAmt,
			PushAmt: pushAmt,
		},
	)

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = net.Alice.WaitForNetworkChannelOpen(ctxt, chanPoint3)
	if err != nil {
		t.Fatalf("alice didn't report channel: %v", err)
	}
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	err = carol.WaitForNetworkChannelOpen(ctxt, chanPoint3)
	if err != nil {
		t.Fatalf("bob didn't report channel: %v", err)
	}

	// Make a global update, and check that both channels' new policies get
	// propagated.
	baseFee = int64(800)
	feeRate = int64(123)
	timeLockDelta = uint32(22)
	maxHtlc *= 2

	expectedPolicy.FeeBaseMAtoms = baseFee
	expectedPolicy.FeeRateMilliMAtoms = testFeeBase * feeRate
	expectedPolicy.TimeLockDelta = timeLockDelta
	expectedPolicy.MaxHtlcMAtoms = maxHtlc

	req = &lnrpc.PolicyUpdateRequest{
		BaseFeeMAtoms: baseFee,
		FeeRate:       float64(feeRate),
		TimeLockDelta: timeLockDelta,
		MaxHtlcMAtoms: maxHtlc,
	}
	req.Scope = &lnrpc.PolicyUpdateRequest_Global{}

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	_, err = net.Alice.UpdateChannelPolicy(ctxt, req)
	if err != nil {
		t.Fatalf("unable to update alice's channel policy: %v", err)
	}

	// Wait for all nodes to have seen the policy updates for both of
	// Alice's channels.
	for _, graphSub := range graphSubs {
		waitForChannelUpdate(
			t, graphSub,
			[]expectedChanUpdate{
				{net.Alice.PubKeyStr, expectedPolicy, chanPoint},
				{net.Alice.PubKeyStr, expectedPolicy, chanPoint3},
			},
		)
	}

	// And finally check that all nodes remembers the policy update they
	// received.
	for _, node := range nodes {
		assertChannelPolicy(
			t, node, net.Alice.PubKeyStr, expectedPolicy,
			chanPoint, chanPoint3,
		)
	}

	// Now, to test that Carol is properly rate limiting incoming updates,
	// we'll send two more update from Alice. Carol should accept the first,
	// but not the second, as she only allows two updates per day and a day
	// has yet to elapse from the previous update.
	const numUpdatesTilRateLimit = 2
	for i := 0; i < numUpdatesTilRateLimit; i++ {
		prevAlicePolicy := *expectedPolicy
		baseFee *= 2
		expectedPolicy.FeeBaseMAtoms = baseFee
		req.BaseFeeMAtoms = baseFee

		ctxt, cancel := context.WithTimeout(ctxb, defaultTimeout)
		defer cancel()
		_, err = net.Alice.UpdateChannelPolicy(ctxt, req)
		if err != nil {
			t.Fatalf("unable to update alice's channel policy: %v", err)
		}

		// Wait for all nodes to have seen the policy updates for both
		// of Alice's channels. Carol will not see the last update as
		// the limit has been reached.
		for idx, graphSub := range graphSubs {
			expUpdates := []expectedChanUpdate{
				{net.Alice.PubKeyStr, expectedPolicy, chanPoint},
				{net.Alice.PubKeyStr, expectedPolicy, chanPoint3},
			}
			// Carol was added last, which is why we check the last
			// index.
			if i == numUpdatesTilRateLimit-1 && idx == len(graphSubs)-1 {
				expUpdates = nil
			}
			waitForChannelUpdate(t, graphSub, expUpdates)
		}

		// And finally check that all nodes remembers the policy update
		// they received. Since Carol didn't receive the last update,
		// she still has Alice's old policy.
		for idx, node := range nodes {
			policy := expectedPolicy
			// Carol was added last, which is why we check the last
			// index.
			if i == numUpdatesTilRateLimit-1 && idx == len(nodes)-1 {
				policy = &prevAlicePolicy
			}
			assertChannelPolicy(
				t, node, net.Alice.PubKeyStr, policy, chanPoint,
				chanPoint3,
			)
		}
	}

	// Close the channels.
	closeChannelAndAssert(t, net, net.Alice, chanPoint, false)
	closeChannelAndAssert(t, net, net.Bob, chanPoint2, false)
	closeChannelAndAssert(t, net, net.Alice, chanPoint3, false)
}

// updateChannelPolicy updates the channel policy of node to the
// given fees and timelock delta. This function blocks until
// listenerNode has received the policy update.
func updateChannelPolicy(t *harnessTest, node *lntest.HarnessNode,
	chanPoint *lnrpc.ChannelPoint, baseFee int64, feeRate int64,
	timeLockDelta uint32, maxHtlc uint64, listenerNode *lntest.HarnessNode) {

	ctxb := context.Background()

	expectedPolicy := &lnrpc.RoutingPolicy{
		FeeBaseMAtoms:      baseFee,
		FeeRateMilliMAtoms: feeRate,
		TimeLockDelta:      timeLockDelta,
		MinHtlc:            1000, // default value
		MaxHtlcMAtoms:      maxHtlc,
	}

	updateFeeReq := &lnrpc.PolicyUpdateRequest{
		BaseFeeMAtoms: baseFee,
		FeeRate:       float64(feeRate) / testFeeBase,
		TimeLockDelta: timeLockDelta,
		Scope: &lnrpc.PolicyUpdateRequest_ChanPoint{
			ChanPoint: chanPoint,
		},
		MaxHtlcMAtoms: maxHtlc,
	}

	// Create the subscription before sending the update so we're certain
	// to get it.
	ctxt, _ := context.WithTimeout(ctxb, defaultTimeout)
	graphSub := subscribeGraphNotifications(ctxt, t, listenerNode)
	defer close(graphSub.quit)

	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	if _, err := node.UpdateChannelPolicy(ctxt, updateFeeReq); err != nil {
		t.Fatalf("unable to update chan policy: %v", err)
	}

	waitForChannelUpdate(
		t, graphSub,
		[]expectedChanUpdate{
			{node.PubKeyStr, expectedPolicy, chanPoint},
		},
	)
}
