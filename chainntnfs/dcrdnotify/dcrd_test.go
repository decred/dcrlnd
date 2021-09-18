//go:build dev
// +build dev

package dcrdnotify

import (
	"bytes"
	"context"
	"io/ioutil"
	"testing"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/internal/testutils"
	rpctest "github.com/decred/dcrtest/dcrdtest"
	"github.com/stretchr/testify/require"
	"matheusd.com/testctx"
)

var (
	testScript = []byte{
		// OP_HASH160
		0xA9,
		// OP_DATA_20
		0x14,
		// <20-byte hash>
		0xec, 0x6f, 0x7a, 0x5a, 0xa8, 0xf2, 0xb1, 0x0c, 0xa5, 0x15,
		0x04, 0x52, 0x3a, 0x60, 0xd4, 0x03, 0x06, 0xf6, 0x96, 0xcd,
		// OP_EQUAL
		0x87,
	}

	netParams = chaincfg.SimNetParams()
)

func initHintCache(t *testing.T) *chainntnfs.HeightHintCache {
	t.Helper()

	tempDir, err := ioutil.TempDir("", "kek")
	if err != nil {
		t.Fatalf("unable to create temp dir: %v", err)
	}
	db, err := channeldb.Open(tempDir)
	if err != nil {
		t.Fatalf("unable to create db: %v", err)
	}
	testCfg := chainntnfs.CacheConfig{
		QueryDisable: false,
	}
	hintCache, err := chainntnfs.NewHeightHintCache(testCfg, db.Backend)
	if err != nil {
		t.Fatalf("unable to create hint cache: %v", err)
	}

	return hintCache
}

// setUpNotifier is a helper function to start a new notifier backed by a dcrd
// driver.
func setUpNotifier(t *testing.T, h *rpctest.Harness) *DcrdNotifier {
	hintCache := initHintCache(t)

	rpcConfig := h.RPCConfig()
	notifier, err := New(&rpcConfig, netParams, hintCache, hintCache, nil)
	if err != nil {
		t.Fatalf("unable to create notifier: %v", err)
	}
	if err := notifier.Start(); err != nil {
		t.Fatalf("unable to start notifier: %v", err)
	}

	return notifier
}

// TestHistoricalConfDetailsTxIndex ensures that we correctly retrieve
// historical confirmation details using the backend node's txindex.
func TestHistoricalConfDetailsTxIndex(t *testing.T) {
	t.Parallel()

	harness, err := testutils.NewSetupRPCTest(
		testctx.New(t), 5, netParams, nil, []string{"--txindex"}, true, 25,
	)
	require.NoError(t, err)
	defer harness.TearDown()

	notifier := setUpNotifier(t, harness)
	defer notifier.Stop()

	// A transaction unknown to the node should not be found within the
	// txindex even if it is enabled, so we should not proceed with any
	// fallback methods.
	var unknownHash chainhash.Hash
	copy(unknownHash[:], bytes.Repeat([]byte{0x10}, 32))
	unknownConfReq, err := chainntnfs.NewConfRequest(&unknownHash, testScript)
	if err != nil {
		t.Fatalf("unable to create conf request: %v", err)
	}
	_, txStatus, err := notifier.historicalConfDetails(unknownConfReq, 0, 0)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	switch txStatus {
	case chainntnfs.TxNotFoundIndex:
	case chainntnfs.TxNotFoundManually:
		t.Fatal("should not have proceeded with fallback method, but did")
	default:
		t.Fatal("should not have found non-existent transaction, but did")
	}

	// Now, we'll create a test transaction and attempt to retrieve its
	// confirmation details.
	txid, pkScript, err := chainntnfs.GetTestTxidAndScript(harness)
	if err != nil {
		t.Fatalf("unable to create tx: %v", err)
	}
	if err := chainntnfs.WaitForMempoolTx(harness, txid); err != nil {
		t.Fatalf("unable to find tx in the mempool: %v", err)
	}
	confReq, err := chainntnfs.NewConfRequest(txid, pkScript)
	if err != nil {
		t.Fatalf("unable to create conf request: %v", err)
	}

	// The transaction should be found in the mempool at this point.
	_, txStatus, err = notifier.historicalConfDetails(confReq, 0, 0)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	// Since it has yet to be included in a block, it should have been found
	// within the mempool.
	switch txStatus {
	case chainntnfs.TxFoundMempool:
	default:
		t.Fatalf("should have found the transaction within the "+
			"mempool, but did not: %v", txStatus)
	}

	// We'll now confirm this transaction and re-attempt to retrieve its
	// confirmation details.
	if _, err := rpctest.AdjustedSimnetMiner(context.Background(), harness.Node, 1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	_, txStatus, err = notifier.historicalConfDetails(confReq, 0, 0)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	// Since the backend node's txindex is enabled and the transaction has
	// confirmed, we should be able to retrieve it using the txindex.
	switch txStatus {
	case chainntnfs.TxFoundIndex:
	default:
		t.Fatal("should have found the transaction within the " +
			"txindex, but did not")
	}
}

// TestHistoricalConfDetailsNoTxIndex ensures that we correctly retrieve
// historical confirmation details using the set of fallback methods when the
// backend node's txindex is disabled.
//
// TODO(decred) rpctest currently always creates nodes with --txindex and
// --addrindex, so this test can't be executed at this time. It can manually
// verified by locally modifying a copy of rpctest and adding a replace
// directive in the top level go.mod file. Commenting this test for the moment.
/*
func TestHistoricalConfDetailsNoTxIndex(t *testing.T) {
	t.Parallel()

	harness, err := testutils.NewSetupRPCTest(
		testctx.New(t), 5, netParams, nil, []string{"--txindex"}, true, 25,
	)
	require.NoError(t, err)
	defer harness.TearDown()

	notifier := setUpNotifier(t, harness)
	defer notifier.Stop()

	// Since the node has its txindex disabled, we fall back to scanning the
	// chain manually. A transaction unknown to the network should not be
	// found.
	var unknownHash chainhash.Hash
	copy(unknownHash[:], bytes.Repeat([]byte{0x10}, 32))
	unknownConfReq, err := chainntnfs.NewConfRequest(&unknownHash, testScript)
	if err != nil {
		t.Fatalf("unable to create conf request: %v", err)
	}
	_, txStatus, err := notifier.historicalConfDetails(unknownConfReq, 0, 0)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	switch txStatus {
	case chainntnfs.TxNotFoundManually:
	case chainntnfs.TxNotFoundIndex:
		t.Fatal("should have proceeded with fallback method, but did not")
	default:
		t.Fatal("should not have found non-existent transaction, but did")
	}

	// Now, we'll create a test transaction and attempt to retrieve its
	// confirmation details. We'll note its broadcast height to use as the
	// height hint when manually scanning the chain.
	_, currentHeight, err := harness.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to retrieve current height: %v", err)
	}

	txid, pkScript, err := chainntnfs.GetTestTxidAndScript(harness)
	if err != nil {
		t.Fatalf("unable to create tx: %v", err)
	}
	if err := chainntnfs.WaitForMempoolTx(harness, txid); err != nil {
		t.Fatalf("unable to find tx in the mempool: %v", err)
	}
	confReq, err := chainntnfs.NewConfRequest(txid, pkScript)
	if err != nil {
		t.Fatalf("unable to create conf request: %v", err)
	}

	_, txStatus, err = notifier.historicalConfDetails(confReq, 0, 0)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	// Since it has yet to be included in a block, it should have been found
	// within the mempool.
	if txStatus != chainntnfs.TxFoundMempool {
		t.Fatal("should have found the transaction within the " +
			"mempool, but did not")
	}

	// We'll now confirm this transaction and re-attempt to retrieve its
	// confirmation details.
	if _, err := harness.Node.Generate(1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	_, txStatus, err = notifier.historicalConfDetails(
		confReq, uint32(currentHeight), uint32(currentHeight)+1,
	)
	if err != nil {
		t.Fatalf("unable to retrieve historical conf details: %v", err)
	}

	// Since the backend node's txindex is disabled and the transaction has
	// confirmed, we should be able to find it by falling back to scanning
	// the chain manually.
	if txStatus != chainntnfs.TxFoundManually {
		t.Fatal("should have found the transaction by manually " +
			"scanning the chain, but did not")
	}
}
*/

// TestInneficientRescan tests whether the inneficient per block rescan works
// as required to detect spent outpoints and scripts.
func TestInneficientRescan(t *testing.T) {
	t.Parallel()

	harness, err := testutils.NewSetupRPCTest(
		testctx.New(t), 5, netParams, nil, []string{"--txindex"}, true, 25,
	)
	require.NoError(t, err)
	defer harness.TearDown()

	notifier := setUpNotifier(t, harness)
	defer notifier.Stop()

	// Create an output and subsequently spend it.
	outpoint, txout, privKey := chainntnfs.CreateSpendableOutput(
		t, harness, nil,
	)
	spenderTx := chainntnfs.CreateSpendTx(
		t, outpoint, txout, privKey,
	)
	spenderTxHash := spenderTx.TxHash()
	_, err = harness.Node.SendRawTransaction(context.TODO(), spenderTx, true)
	if err != nil {
		t.Fatalf("unable to publish tx: %v", err)
	}
	if err := chainntnfs.WaitForMempoolTx(harness, &spenderTxHash); err != nil {
		t.Fatalf("unable to find tx in the mempool: %v", err)
	}

	// We'll now confirm this transaction and attempt to retrieve its
	// confirmation details.
	bhs, err := rpctest.AdjustedSimnetMiner(context.Background(), harness.Node, 1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}
	block, err := harness.Node.GetBlock(context.TODO(), bhs[0])
	if err != nil {
		t.Fatalf("unable to get block: %v", err)
	}
	var testTx *wire.MsgTx
	for _, tx := range block.Transactions {
		otherHash := tx.TxHash()
		if spenderTxHash.IsEqual(&otherHash) {
			testTx = tx
			break
		}
	}
	if testTx == nil {
		t.Fatalf("test transaction was not mined")
	}
	minedHeight := int64(block.Header.Height)
	prevOutputHeight := minedHeight - 1

	// Generate a few blocks after mining to test some conditions.
	if _, err := rpctest.AdjustedSimnetMiner(context.Background(), harness.Node, 20); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// Store some helper constants.
	endHeight := minedHeight + 20
	pkScript, err := chainscan.ParsePkScript(txout.Version, txout.PkScript)
	if err != nil {
		t.Fatalf("unable to parse pkscript: %v", err)
	}
	_, addrs := stdscript.ExtractAddrs(
		txout.Version, txout.PkScript, netParams,
	)
	if len(addrs) != 1 {
		t.Fatalf("wrong nb of addrs: %d", len(addrs))
	}
	addr := addrs[0]

	// These are the individual cases to test.
	testCases := []struct {
		name       string
		start      int64
		shouldFind bool
	}{
		{
			name:       "at mined block",
			start:      minedHeight,
			shouldFind: true,
		},
		{
			name:       "long before mined",
			start:      minedHeight - 20,
			shouldFind: true,
		},
		{
			name:       "just before prevout is mined",
			start:      prevOutputHeight - 1,
			shouldFind: true,
		},
		{
			name:       "just before mined",
			start:      minedHeight - 1,
			shouldFind: true,
		},
		{
			name:       "at next block",
			start:      minedHeight + 1,
			shouldFind: false,
		},
		{
			name:       "long after the mined block",
			start:      minedHeight + 20,
			shouldFind: false,
		},
	}

	// We'll test both scanning for an output and a pkscript for each of
	// the previous tests.
	spendReqTestCases := []struct {
		name      string
		spendReq  chainntnfs.SpendRequest
		addrs     []stdaddr.Address
		outpoints []wire.OutPoint
	}{
		{
			name: "by outpoint",
			spendReq: chainntnfs.SpendRequest{
				OutPoint: *outpoint,
			},
			outpoints: []wire.OutPoint{*outpoint},
		},
		{
			name: "by pkScript",
			spendReq: chainntnfs.SpendRequest{
				PkScript: pkScript,
			},
			addrs: []stdaddr.Address{addr},
		},
	}

	for _, stc := range spendReqTestCases {
		success := t.Run(stc.name, func(t2 *testing.T) {
			spendReq := stc.spendReq

			// Load the tx filter with the appropriate outpoint or
			// address as preparation for the tests.
			err := notifier.chainConn.LoadTxFilter(
				context.TODO(), true, stc.addrs, stc.outpoints,
			)
			if err != nil {
				t.Fatalf("unable to build tx filter: %v", err)
			}

			for _, tc := range testCases {
				success := t2.Run(tc.name, func(t3 *testing.T) {
					histDispatch := chainntnfs.HistoricalSpendDispatch{
						SpendRequest: spendReq,
						StartHeight:  uint32(tc.start),
						EndHeight:    uint32(endHeight),
					}

					details, err := notifier.inefficientSpendRescan(
						histDispatch.StartHeight, &histDispatch,
					)

					switch {
					case tc.shouldFind && details == nil:
						t3.Fatalf("should find tx but did not get "+
							"details (%v)", err)
					case !tc.shouldFind && details != nil:
						t3.Fatalf("should not find tx but got details")
					case !tc.shouldFind && err != errInefficientRescanTxNotFound:
						t3.Fatalf("should not find tx but got unexpected error %v", err)
					}

				})
				if !success {
					break
				}
			}
		})

		if !success {
			break
		}
	}
}
