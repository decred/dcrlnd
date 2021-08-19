package itest

import (
	"context"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/funding"
	"github.com/decred/dcrlnd/lntest"
	"github.com/stretchr/testify/require"
)

// testMaxChannelSize tests that lnd handles --maxchansize parameter
// correctly. Wumbo nodes should enforce a default soft limit of 10 BTC by
// default. This limit can be adjusted with --maxchansize config option
func testMaxChannelSize(net *lntest.NetworkHarness, t *harnessTest) {

	// Mine blocks until the miner wallet has enough funds to start the test.
	var blockCount int
	for net.Miner.ConfirmedBalance() < 520e8 {
		_, err := net.Generate(5)
		require.NoError(t.t, err)
		blockCount += 5
		if blockCount > 1000 {
			t.Fatalf("mined too many blocks")
		}
	}

	// We'll make two new nodes, both wumbo but with the default limit on
	// maximum channel size (500 DCR)
	wumboNode := net.NewNode(
		t.t, "wumbo", []string{"--protocol.wumbo-channels"},
	)
	defer shutdownAndAssert(net, t, wumboNode)

	wumboNode2 := net.NewNode(
		t.t, "wumbo2", []string{"--protocol.wumbo-channels"},
	)
	defer shutdownAndAssert(net, t, wumboNode2)

	// We'll send 501 DCR to the wumbo node so it can test the wumbo soft
	// limit.
	ctxb := context.Background()
	net.SendCoins(t.t, 501*dcrutil.AtomsPerCoin, wumboNode)

	// Next we'll connect both nodes, then attempt to make a wumbo channel
	// funding request, which should fail as it exceeds the default wumbo
	// soft limit of 500 DCR.
	net.EnsureConnected(ctxb, t.t, wumboNode, wumboNode2)

	chanAmt := funding.MaxDecredFundingAmountWumbo + 1
	_, err := net.OpenChannel(
		ctxb, wumboNode, wumboNode2, lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)
	if err == nil {
		t.Fatalf("expected channel funding to fail as it exceeds 10 BTC limit")
	}

	// The test should show failure due to the channel exceeding our max size.
	if !strings.Contains(err.Error(), "exceeds maximum chan size") {
		t.Fatalf("channel should be rejected due to size, instead "+
			"error was: %v", err)
	}

	// Next we'll create a non-wumbo node to verify that it enforces the
	// BOLT-02 channel size limit and rejects our funding request.
	miniNode := net.NewNode(t.t, "mini", nil)
	defer shutdownAndAssert(net, t, miniNode)

	net.EnsureConnected(ctxb, t.t, wumboNode, miniNode)

	_, err = net.OpenChannel(
		ctxb, wumboNode, miniNode, lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)
	if err == nil {
		t.Fatalf("expected channel funding to fail as it exceeds 10.7 DCR limit")
	}

	// The test should show failure due to the channel exceeding our max size.
	if !strings.Contains(err.Error(), "exceeds maximum chan size") {
		t.Fatalf("channel should be rejected due to size, instead "+
			"error was: %v", err)
	}

	// We'll now make another wumbo node with appropriate maximum channel size
	// to accept our wumbo channel funding.
	wumboNode3 := net.NewNode(
		t.t, "wumbo3", []string{"--protocol.wumbo-channels",
			fmt.Sprintf("--maxchansize=%v", int64(funding.MaxDecredFundingAmountWumbo+1))},
	)
	defer shutdownAndAssert(net, t, wumboNode3)

	// Creating a wumbo channel between these two nodes should succeed.
	net.EnsureConnected(ctxb, t.t, wumboNode, wumboNode3)
	chanPoint := openChannelAndAssert(
		t, net, wumboNode, wumboNode3,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)
	closeChannelAndAssert(t, net, wumboNode, chanPoint, false)

}
