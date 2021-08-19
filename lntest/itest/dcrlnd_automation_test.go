package itest

import (
	"time"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
	"matheusd.com/testctx"
)

func testMissingChanReestablishAutoClosesChan(net *lntest.NetworkHarness, t *harnessTest) {
	const (
		chanAmt = dcrutil.Amount(10000000)
		pushAmt = dcrutil.Amount(5000000)
	)
	password := []byte("El Psy Kongroo")
	var err error

	// Create a new retore scenario.
	carolArgs := []string{"--automation.closechanreestablishwait=5"}
	carol := net.NewNode(t.t, "carol", carolArgs)
	defer shutdownAndAssert(net, t, carol)

	daveArgs := []string{"--nolisten", "--minbackoff=1h"}
	dave, mnemonic, _, err := net.NewNodeWithSeed(
		"dave", daveArgs, password, false,
	)
	require.Nil(t.t, err)
	defer shutdownAndAssert(net, t, dave)

	net.SendCoins(t.t, dcrutil.AtomsPerCoin, carol)
	net.SendCoins(t.t, dcrutil.AtomsPerCoin, dave)
	net.EnsureConnected(testctx.New(t), t.t, dave, carol)

	chanPoint := openChannelAndAssert(
		t, net, carol, dave,
		lntest.OpenChannelParams{
			Amt:     chanAmt,
			PushAmt: pushAmt,
		},
	)

	// Wait for both sides to see the opened channel.
	err = dave.WaitForNetworkChannelOpen(testctx.New(t), chanPoint)
	require.Nil(t.t, err)
	err = carol.WaitForNetworkChannelOpen(testctx.New(t), chanPoint)
	require.Nil(t.t, err)

	// Perform a payment to assert channel is working.
	invoice := &lnrpc.Invoice{
		Memo:  "testing",
		Value: 100000,
	}
	invoiceResp, err := carol.AddInvoice(testctx.New(t), invoice)
	require.Nil(t.t, err)
	err = completePaymentRequests(
		testctx.New(t), dave, dave.RouterClient,
		[]string{invoiceResp.PaymentRequest}, true,
	)
	require.Nil(t.t, err)

	// Recreate Dave without the channel.
	err = net.ShutdownNode(dave)
	require.Nil(t.t, err)
	time.Sleep(time.Second)

	daveRestored, err := net.RestoreNodeWithSeed(
		"dave", nil, password, mnemonic, 1000,
		nil, copyPorts(dave),
	)
	require.Nil(t.t, err)
	assertNumPendingChannels(t, daveRestored, 0, 0, 0, 0)
	assertNodeNumChannels(t, daveRestored, 0)
	// ht.AssertNumEdges(daveRestored, 0, true)

	// Assert Carol does not autoclose and Dave does not have the channel.
	net.EnsureConnected(testctx.New(t), t.t, daveRestored, carol)
	time.Sleep(time.Second)
	assertNumPendingChannels(t, daveRestored, 0, 0, 0, 0)
	assertNodeNumChannels(t, daveRestored, 0)
	assertNumPendingChannels(t, carol, 0, 0, 0, 0)
	assertNodeNumChannels(t, carol, 1)

	// Assert Carol is tracking the time Dave has been online without
	// reestablishing the channel.
	require.Nil(t.t, net.DisconnectNodes(testctx.New(t), carol, daveRestored))
	time.Sleep(time.Second)
	chanInfo, err := carol.ListChannels(testctx.New(t), &lnrpc.ListChannelsRequest{})
	require.Nil(t.t, err)
	require.Len(t.t, chanInfo.Channels, 1)
	require.Greater(t.t, chanInfo.Channels[0].ChanReestablishWaitTimeMs, int64(2000))

	// Wait long enough for Carol's automation to want to force-close the
	// channel.
	net.EnsureConnected(testctx.New(t), t.t, daveRestored, carol)
	time.Sleep(time.Second * 3)
	err = net.ShutdownNode(daveRestored)
	require.Nil(t.t, err)

	// Assert Carol force-closes the channel.
	assertNumPendingChannels(t, carol, 1, 0, 0, 0)
	assertNodeNumChannels(t, carol, 0)
	mineBlocks(t, net, 1, 1)
	cleanupForceClose(t, net, carol, chanPoint)
}
