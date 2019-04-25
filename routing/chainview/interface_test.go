package chainview

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrjson/v2"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/rpcclient/v2"
	"github.com/decred/dcrd/rpctest"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
)

var (
	netParams = &chaincfg.SimNetParams

	testPrivKey = []byte{
		0x81, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
		0x63, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
		0xd, 0xe7, 0x95, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
		0x1e, 0xb, 0x4c, 0xfd, 0x9e, 0xc5, 0x8c, 0xe9,
	}

	privKey, pubKey = secp256k1.PrivKeyFromBytes(testPrivKey)
	addrPk, _       = dcrutil.NewAddressSecpPubKeyCompressed(pubKey,
		netParams)
	testAddr = addrPk.AddressPubKeyHash()

	testScript, _ = txscript.PayToAddrScript(testAddr)
)

func waitForMempoolTx(r *rpctest.Harness, txid *chainhash.Hash) error {
	var found bool
	var tx *dcrutil.Tx
	var err error
	timeout := time.After(10 * time.Second)
	for !found {
		// Do a short wait
		select {
		case <-timeout:
			return fmt.Errorf("timeout after 10s")
		default:
		}
		time.Sleep(100 * time.Millisecond)

		// Check for the harness' knowledge of the txid
		tx, err = r.Node.GetRawTransaction(txid)
		if err != nil {
			switch e := err.(type) {
			case *dcrjson.RPCError:
				if e.Code == dcrjson.ErrRPCNoTxInfo {
					continue
				}
			default:
			}
			return err
		}
		if tx != nil && tx.MsgTx().TxHash() == *txid {
			found = true
		}
	}
	return nil
}

func getTestTXID(miner *rpctest.Harness) (*chainhash.Hash, error) {
	script, err := txscript.PayToAddrScript(testAddr)
	if err != nil {
		return nil, err
	}

	outputs := []*wire.TxOut{
		{
			Value:    2e8,
			PkScript: script,
		},
	}
	return miner.SendOutputs(outputs, 2500)
}

func locateOutput(tx *wire.MsgTx, script []byte) (*wire.OutPoint, *wire.TxOut, error) {
	for i, txOut := range tx.TxOut {
		if bytes.Equal(txOut.PkScript, script) {
			return &wire.OutPoint{
				Hash:  tx.TxHash(),
				Index: uint32(i),
			}, txOut, nil
		}
	}

	return nil, nil, fmt.Errorf("unable to find output")
}

func craftSpendTransaction(outpoint wire.OutPoint, payScript []byte) (*wire.MsgTx, error) {
	spendingTx := wire.NewMsgTx()
	spendingTx.AddTxIn(&wire.TxIn{
		PreviousOutPoint: outpoint,
	})
	spendingTx.AddTxOut(&wire.TxOut{
		Value:    1e8,
		PkScript: payScript,
	})
	sigScript, err := txscript.SignatureScript(spendingTx, 0, payScript,
		txscript.SigHashAll, privKey, true)
	if err != nil {
		return nil, err
	}
	spendingTx.TxIn[0].SignatureScript = sigScript

	return spendingTx, nil
}

func assertFilteredBlock(t *testing.T, fb *FilteredBlock, expectedHeight int64,
	expectedHash *chainhash.Hash, txns []*chainhash.Hash) {

	_, _, line, _ := runtime.Caller(1)

	if fb.Height != expectedHeight {
		t.Fatalf("line %v: block height mismatch: expected %v, got %v",
			line, expectedHeight, fb.Height)
	}
	if !bytes.Equal(fb.Hash[:], expectedHash[:]) {
		t.Fatalf("line %v: block hash mismatch: expected %v, got %v",
			line, expectedHash, fb.Hash)
	}
	if len(fb.Transactions) != len(txns) {
		t.Fatalf("line %v: expected %v transaction in filtered block, instead "+
			"have %v", line, len(txns), len(fb.Transactions))
	}

	expectedTxids := make(map[chainhash.Hash]struct{})
	for _, txn := range txns {
		expectedTxids[*txn] = struct{}{}
	}

	for _, tx := range fb.Transactions {
		txid := tx.TxHash()
		delete(expectedTxids, txid)
	}

	if len(expectedTxids) != 0 {
		t.Fatalf("line %v: missing txids: %v", line, expectedTxids)
	}
}

func testFilterBlockNotifications(node *rpctest.Harness,
	chainView FilteredChainView, chainViewInit chainViewInitFunc,
	t *testing.T) {

	// To start the test, we'll create to fresh outputs paying to the
	// private key that we generated above.
	txid1, err := getTestTXID(node)
	if err != nil {
		t.Fatalf("unable to get test txid1: %v", err)
	}
	err = waitForMempoolTx(node, txid1)
	if err != nil {
		t.Fatalf("unable to get test txid in mempool: %v", err)
	}
	txid2, err := getTestTXID(node)
	if err != nil {
		t.Fatalf("unable to get test txid2: %v", err)
	}
	err = waitForMempoolTx(node, txid2)
	if err != nil {
		t.Fatalf("unable to get test txid in mempool: %v", err)
	}

	blockChan := chainView.FilteredBlocks()

	// Next we'll mine a block confirming the output generated above.
	newBlockHashes, err := node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	_, currentHeight, err := node.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// We should get an update, however it shouldn't yet contain any
	// filtered transaction as the filter hasn't been update.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight,
			newBlockHashes[0], []*chainhash.Hash{})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	// Now that the block has been mined, we'll fetch the two transactions
	// so we can add them to the filter, and also craft transaction
	// spending the outputs we created.
	tx1, err := node.Node.GetRawTransaction(txid1)
	if err != nil {
		t.Fatalf("unable to fetch transaction: %v", err)
	}
	tx2, err := node.Node.GetRawTransaction(txid2)
	if err != nil {
		t.Fatalf("unable to fetch transaction: %v", err)
	}

	targetScript, err := txscript.PayToAddrScript(testAddr)
	if err != nil {
		t.Fatalf("unable to create target output: %v", err)
	}

	// Next, we'll locate the two outputs generated above that pay to use
	// so we can properly add them to the filter.
	outPoint1, _, err := locateOutput(tx1.MsgTx(), targetScript)
	if err != nil {
		t.Fatalf("unable to find output: %v", err)
	}
	outPoint2, _, err := locateOutput(tx2.MsgTx(), targetScript)
	if err != nil {
		t.Fatalf("unable to find output: %v", err)
	}

	_, currentHeight, err = node.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// Now we'll add both outpoints to the current filter.
	filter := []channeldb.EdgePoint{
		{FundingPkScript: targetScript, OutPoint: *outPoint1},
		{FundingPkScript: targetScript, OutPoint: *outPoint2},
	}
	err = chainView.UpdateFilter(filter, currentHeight)
	if err != nil {
		t.Fatalf("unable to update filter: %v", err)
	}

	// With the filter updated, we'll now create two transaction spending
	// the outputs we created.
	spendingTx1, err := craftSpendTransaction(*outPoint1, targetScript)
	if err != nil {
		t.Fatalf("unable to create spending tx: %v", err)
	}
	spendingTx2, err := craftSpendTransaction(*outPoint2, targetScript)
	if err != nil {
		t.Fatalf("unable to create spending tx: %v", err)
	}

	// Now we'll broadcast the first spending transaction and also mine a
	// block which should include it.
	spendTxid1, err := node.Node.SendRawTransaction(spendingTx1, true)
	if err != nil {
		t.Fatalf("unable to broadcast transaction: %v", err)
	}
	err = waitForMempoolTx(node, spendTxid1)
	if err != nil {
		t.Fatalf("unable to get spending txid in mempool: %v", err)
	}
	newBlockHashes, err = node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// We should receive a notification over the channel. The notification
	// should correspond to the current block height and have that single
	// filtered transaction.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight+1,
			newBlockHashes[0], []*chainhash.Hash{spendTxid1})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	// Next, mine the second transaction which spends the second output.
	// This should also generate a notification.
	spendTxid2, err := node.Node.SendRawTransaction(spendingTx2, true)
	if err != nil {
		t.Fatalf("unable to broadcast transaction: %v", err)
	}
	err = waitForMempoolTx(node, spendTxid2)
	if err != nil {
		t.Fatalf("unable to get spending txid in mempool: %v", err)
	}
	newBlockHashes, err = node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight+2,
			newBlockHashes[0], []*chainhash.Hash{spendTxid2})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}
}

func testUpdateFilterBackTrack(node *rpctest.Harness,
	chainView FilteredChainView, chainViewInit chainViewInitFunc,
	t *testing.T) {

	// To start, we'll create a fresh output paying to the height generated
	// above.
	txid, err := getTestTXID(node)
	if err != nil {
		t.Fatalf("unable to get test txid")
	}
	err = waitForMempoolTx(node, txid)
	if err != nil {
		t.Fatalf("unable to get test txid in mempool: %v", err)
	}

	// Next we'll mine a block confirming the output generated above.
	initBlockHashes, err := node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	blockChan := chainView.FilteredBlocks()

	_, currentHeight, err := node.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// Consume the notification sent which contains an empty filtered
	// block.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight,
			initBlockHashes[0], []*chainhash.Hash{})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	// Next, create a transaction which spends the output created above,
	// mining the spend into a block.
	tx, err := node.Node.GetRawTransaction(txid)
	if err != nil {
		t.Fatalf("unable to fetch transaction: %v", err)
	}
	outPoint, _, err := locateOutput(tx.MsgTx(), testScript)
	if err != nil {
		t.Fatalf("unable to find output: %v", err)
	}
	spendingTx, err := craftSpendTransaction(*outPoint, testScript)
	if err != nil {
		t.Fatalf("unable to create spending tx: %v", err)
	}
	spendTxid, err := node.Node.SendRawTransaction(spendingTx, true)
	if err != nil {
		t.Fatalf("unable to broadcast transaction: %v", err)
	}
	err = waitForMempoolTx(node, spendTxid)
	if err != nil {
		t.Fatalf("unable to get spending txid in mempool: %v", err)
	}
	newBlockHashes, err := node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// We should have received another empty filtered block notification.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight+1,
			newBlockHashes[0], []*chainhash.Hash{})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	// After the block has been mined+notified we'll update the filter with
	// a _prior_ height so a "rewind" occurs.
	filter := []channeldb.EdgePoint{
		{FundingPkScript: testScript, OutPoint: *outPoint},
	}
	err = chainView.UpdateFilter(filter, currentHeight)
	if err != nil {
		t.Fatalf("unable to update filter: %v", err)
	}

	// We should now receive a fresh filtered block notification that
	// includes the transaction spend we included above.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight+1,
			newBlockHashes[0], []*chainhash.Hash{spendTxid})
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}
}

func testFilterSingleBlock(node *rpctest.Harness, chainView FilteredChainView,
	chainViewInit chainViewInitFunc, t *testing.T) {

	// In this test, we'll test the manual filtration of blocks, which can
	// be used by clients to manually rescan their sub-set of the UTXO set.

	// First, we'll create a block that includes two outputs that we're
	// able to spend with the private key generated above.
	txid1, err := getTestTXID(node)
	if err != nil {
		t.Fatalf("unable to get test txid")
	}
	err = waitForMempoolTx(node, txid1)
	if err != nil {
		t.Fatalf("unable to get test txid in mempool: %v", err)
	}
	txid2, err := getTestTXID(node)
	if err != nil {
		t.Fatalf("unable to get test txid")
	}
	err = waitForMempoolTx(node, txid2)
	if err != nil {
		t.Fatalf("unable to get test txid in mempool: %v", err)
	}

	blockChan := chainView.FilteredBlocks()

	// Next we'll mine a block confirming the output generated above.
	newBlockHashes, err := node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	_, currentHeight, err := node.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// We should get an update, however it shouldn't yet contain any
	// filtered transaction as the filter hasn't been updated.
	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight,
			newBlockHashes[0], nil)
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	tx1, err := node.Node.GetRawTransaction(txid1)
	if err != nil {
		t.Fatalf("unable to fetch transaction: %v", err)
	}
	tx2, err := node.Node.GetRawTransaction(txid2)
	if err != nil {
		t.Fatalf("unable to fetch transaction: %v", err)
	}

	// Next, we'll create a block that includes two transactions, each
	// which spend one of the outputs created.
	outPoint1, _, err := locateOutput(tx1.MsgTx(), testScript)
	if err != nil {
		t.Fatalf("unable to find output: %v", err)
	}
	outPoint2, _, err := locateOutput(tx2.MsgTx(), testScript)
	if err != nil {
		t.Fatalf("unable to find output: %v", err)
	}
	spendingTx1, err := craftSpendTransaction(*outPoint1, testScript)
	if err != nil {
		t.Fatalf("unable to create spending tx1: %v", err)
	}
	spendingTx2, err := craftSpendTransaction(*outPoint2, testScript)
	if err != nil {
		t.Fatalf("unable to create spending tx2: %v", err)
	}
	_, err = node.Node.SendRawTransaction(spendingTx1, true)
	if err != nil {
		t.Fatalf("unable to send spending tx1: %v", err)
	}
	_, err = node.Node.SendRawTransaction(spendingTx2, true)
	if err != nil {
		t.Fatalf("unable to send spending tx2: %v", err)
	}
	blockHashes, err := node.Node.Generate(1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	select {
	case filteredBlock := <-blockChan:
		assertFilteredBlock(t, filteredBlock, currentHeight+1,
			blockHashes[0], nil)
	case <-time.After(time.Second * 20):
		t.Fatalf("filtered block notification didn't arrive")
	}

	_, currentHeight, err = node.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// Now we'll manually trigger filtering the block generated above.
	// First, we'll add the two outpoints to our filter.
	filter := []channeldb.EdgePoint{
		{FundingPkScript: testScript, OutPoint: *outPoint1},
		{FundingPkScript: testScript, OutPoint: *outPoint2},
	}
	err = chainView.UpdateFilter(filter, currentHeight)
	if err != nil {
		t.Fatalf("unable to update filter: %v", err)
	}

	// We set the filter with the current height, so we shouldn't get any
	// notifications.
	select {
	case <-blockChan:
		t.Fatalf("got filter notification, but shouldn't have")
	default:
	}

	// Now we'll manually rescan that past block. This should include two
	// filtered transactions, the spending transactions we created above.
	filteredBlock, err := chainView.FilterBlock(blockHashes[0])
	if err != nil {
		t.Fatalf("unable to filter block: %v", err)
	}
	txn1, txn2 := spendingTx1.TxHash(), spendingTx2.TxHash()
	expectedTxns := []*chainhash.Hash{&txn1, &txn2}
	assertFilteredBlock(t, filteredBlock, currentHeight, blockHashes[0],
		expectedTxns)
}

// testFilterBlockDisconnected triggers a reorg all the way back to genesis,
// and a small 5 block reorg, ensuring the chainView notifies about
// disconnected and connected blocks in the order we expect.
func testFilterBlockDisconnected(node *rpctest.Harness,
	chainView FilteredChainView, chainViewInit chainViewInitFunc,
	t *testing.T) {

	// Create a node that has a shorter chain than the main chain, so we
	// can trigger a reorg.
	reorgNode, err := rpctest.New(netParams, nil, []string{"--txindex"})
	if err != nil {
		t.Fatalf("unable to create mining node: %v", err)
	}
	defer reorgNode.TearDown()

	// This node's chain will be maturity+5 blocks.
	if err := reorgNode.SetUp(true, 5); err != nil {
		t.Fatalf("unable to set up mining node: %v", err)
	}

	// Init a chain view that has this node as its block source.
	cleanUpFunc, reorgView, err := chainViewInit(reorgNode.RPCConfig())
	if err != nil {
		t.Fatalf("unable to create chain view: %v", err)
	}
	defer func() {
		if cleanUpFunc != nil {
			cleanUpFunc()
		}
	}()

	if err = reorgView.Start(); err != nil {
		t.Fatalf("unable to start dcrd chain view: %v", err)
	}
	defer reorgView.Stop()

	newBlocks := reorgView.FilteredBlocks()
	disconnectedBlocks := reorgView.DisconnectedBlocks()

	_, oldHeight, err := reorgNode.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// Now connect the node with the short chain to the main node, and wait
	// for their chains to synchronize. The short chain will be reorged all
	// the way back to genesis.
	if err := rpctest.ConnectNode(reorgNode, node); err != nil {
		t.Fatalf("unable to connect harnesses: %v", err)
	}
	nodeSlice := []*rpctest.Harness{node, reorgNode}
	if err := rpctest.JoinNodes(nodeSlice, rpctest.Blocks); err != nil {
		t.Fatalf("unable to join node on blocks: %v", err)
	}

	_, newHeight, err := reorgNode.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// We should be getting oldHeight number of blocks marked as
	// stale/disconnected. We expect to first get all stale blocks,
	// then the new blocks. We also ensure a strict ordering.
	for i := int64(0); i < oldHeight+newHeight; i++ {
		select {
		case block := <-newBlocks:
			if i < oldHeight {
				t.Fatalf("did not expect to get new block "+
					"in iteration %d, old height: %v", i,
					oldHeight)
			}
			expectedHeight := i - oldHeight + 1
			if block.Height != expectedHeight {
				t.Fatalf("expected to receive connected "+
					"block at height %d, instead got at %d",
					expectedHeight, block.Height)
			}
		case block := <-disconnectedBlocks:
			if i >= oldHeight {
				t.Fatalf("did not expect to get stale block "+
					"in iteration %d", i)
			}
			expectedHeight := oldHeight - i
			if block.Height != expectedHeight {
				t.Fatalf("expected to receive disconnected "+
					"block at height %d, instead got at %d",
					expectedHeight, block.Height)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("timeout waiting for block")
		}
	}

	// Now we trigger a small reorg, by disconnecting the nodes, mining
	// a few blocks on each, then connecting them again.
	peers, err := reorgNode.Node.GetPeerInfo()
	if err != nil {
		t.Fatalf("unable to get peer info: %v", err)
	}
	numPeers := len(peers)

	// TODO(decred): This is hacky.  Ideally there should be a way to get the
	// peer address from the passed in node directly rather than assuming it
	// is the first connected peer which is brittle if the tests change.
	//
	// Disconnect the nodes.
	if numPeers < 1 {
		t.Fatalf("no connected peer")
	}
	err = reorgNode.Node.AddNode(peers[0].Addr, rpcclient.ANRemove)
	if err != nil {
		t.Fatalf("unable to disconnect mining nodes: %v", err)
	}

	// Wait for disconnection
	for {
		peers, err = reorgNode.Node.GetPeerInfo()
		if err != nil {
			t.Fatalf("unable to get peer info: %v", err)
		}
		if len(peers) < numPeers {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Mine 10 blocks on the main chain, 5 on the chain that will be
	// reorged out,
	node.Node.Generate(10)
	reorgNode.Node.Generate(5)

	// 5 new blocks should get notified.
	for i := int64(0); i < 5; i++ {
		select {
		case block := <-newBlocks:
			expectedHeight := newHeight + i + 1
			if block.Height != expectedHeight {
				t.Fatalf("expected to receive connected "+
					"block at height %d, instead got at %d",
					expectedHeight, block.Height)
			}
		case <-disconnectedBlocks:
			t.Fatalf("did not expect to get stale block "+
				"in iteration %d", i)
		case <-time.After(10 * time.Second):
			t.Fatalf("did not get connected block")
		}
	}

	_, oldHeight, err = reorgNode.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// Now connect the two nodes, and wait for their chains to sync up.
	if err := rpctest.ConnectNode(reorgNode, node); err != nil {
		t.Fatalf("unable to connect harnesses: %v", err)
	}
	if err := rpctest.JoinNodes(nodeSlice, rpctest.Blocks); err != nil {
		t.Fatalf("unable to join node on blocks: %v", err)
	}

	_, _, err = reorgNode.Node.GetBestBlock()
	if err != nil {
		t.Fatalf("unable to get current height: %v", err)
	}

	// We should get 5 disconnected, 10 connected blocks.
	for i := int64(0); i < 15; i++ {
		select {
		case block := <-newBlocks:
			if i < 5 {
				t.Fatalf("did not expect to get new block "+
					"in iteration %d", i)
			}
			// The expected height for the connected block will be
			// oldHeight - 5 (the 5 disconnected blocks) + (i-5)
			// (subtract 5 since the 5 first iterations consumed
			// disconnected blocks) + 1
			expectedHeight := oldHeight - 9 + i
			if block.Height != expectedHeight {
				t.Fatalf("expected to receive connected "+
					"block at height %d, instead got at %d",
					expectedHeight, block.Height)
			}
		case block := <-disconnectedBlocks:
			if i >= 5 {
				t.Fatalf("did not expect to get stale block "+
					"in iteration %d", i)
			}
			expectedHeight := oldHeight - i
			if block.Height != expectedHeight {
				t.Fatalf("expected to receive disconnected "+
					"block at height %d, instead got at %d",
					expectedHeight, block.Height)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("did not get disconnected block")
		}
	}

	// Time for db access to finish between testcases.
	time.Sleep(time.Millisecond * 500)
}

type chainViewInitFunc func(rpcInfo rpcclient.ConnConfig) (func(), FilteredChainView, error)

type testCase struct {
	name string
	test func(*rpctest.Harness, FilteredChainView, chainViewInitFunc,
		*testing.T)
}

var chainViewTests = []testCase{
	{
		name: "filtered block ntfns",
		test: testFilterBlockNotifications,
	},
	{
		name: "update filter back track",
		test: testUpdateFilterBackTrack,
	},
	{
		name: "filter single block",
		test: testFilterSingleBlock,
	},
	{
		name: "filter block disconnected",
		test: testFilterBlockDisconnected,
	},
}

var interfaceImpls = []struct {
	name          string
	chainViewInit chainViewInitFunc
}{
	{
		name: "dcrd_websockets",
		chainViewInit: func(config rpcclient.ConnConfig) (func(), FilteredChainView, error) {
			chainView, err := NewDcrdFilteredChainView(config)
			if err != nil {
				return nil, nil, err
			}

			return nil, chainView, err
		},
	},
}

func TestFilteredChainView(t *testing.T) {
	// Initialize the harness around a dcrd node which will serve as our
	// dedicated miner to generate blocks, cause re-orgs, etc. We'll set up
	// this node with a chain length of 125, so we have plenty of DCR to
	// play around with.
	miner, err := rpctest.New(netParams, nil, []string{"--txindex"})
	if err != nil {
		t.Fatalf("unable to create mining node: %v", err)
	}
	defer miner.TearDown()
	if err := miner.SetUp(true, 25); err != nil {
		t.Fatalf("unable to set up mining node: %v", err)
	}

	rpcConfig := miner.RPCConfig()

	for _, chainViewImpl := range interfaceImpls {
		t.Logf("Testing '%v' implementation of FilteredChainView",
			chainViewImpl.name)

		cleanUpFunc, chainView, err := chainViewImpl.chainViewInit(rpcConfig)
		if err != nil {
			t.Fatalf("unable to make chain view: %v", err)
		}

		if err := chainView.Start(); err != nil {
			t.Fatalf("unable to start chain view: %v", err)
		}
		for _, chainViewTest := range chainViewTests {
			testName := fmt.Sprintf("%v: %v", chainViewImpl.name,
				chainViewTest.name)
			success := t.Run(testName, func(t *testing.T) {
				chainViewTest.test(miner, chainView,
					chainViewImpl.chainViewInit, t)
			})

			if !success {
				break
			}
		}

		if err := chainView.Stop(); err != nil {
			t.Fatalf("unable to stop chain view: %v", err)
		}

		if cleanUpFunc != nil {
			cleanUpFunc()
		}
	}
}
