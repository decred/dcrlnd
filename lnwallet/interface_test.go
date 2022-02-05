package lnwallet_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/decred/dcrlnd/chainntnfs/dcrdnotify"

	_ "decred.org/dcrwallet/v2/wallet/drivers/bdb"
	"decred.org/dcrwallet/v2/wallet/txsizes"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrjson/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	dcrutilv3 "github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/rpcclient/v7"
	"github.com/decred/dcrd/rpctest"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/stretchr/testify/require"

	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/channeldb/kvdb"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/internal/testutils"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/labels"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwallet/chanfunding"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"
	"github.com/decred/dcrlnd/lnwallet/remotedcrwallet"
	"github.com/decred/dcrlnd/lnwire"
)

var (
	bobsPrivKey = []byte{
		0x81, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
		0x63, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
		0xd, 0xe7, 0x95, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
		0x1e, 0xb, 0x4c, 0xfd, 0x9e, 0xc5, 0x8c, 0xe9,
	}

	// Use a hard-coded HD seed.
	testHdSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x88, 0xa3, 0xef, 0xb9,
		0x6a, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}

	aliceHDSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x18, 0xa3, 0xef, 0xb9,
		0x64, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}
	bobHDSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x98, 0xa3, 0xef, 0xb9,
		0x69, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}

	netParams = chaincfg.SimNetParams()
	chainHash = &netParams.GenesisHash

	alicePub = secp256k1.PrivKeyFromBytes(testHdSeed[:]).PubKey()
	bobPub   = secp256k1.PrivKeyFromBytes(bobsPrivKey).PubKey()

	// The number of confirmations required to consider any created channel
	// open.
	numReqConfs uint16 = 1

	csvDelay uint16 = 4

	scriptVersion uint16 = 0

	bobAddr, _   = net.ResolveTCPAddr("tcp", "10.0.0.2:9000")
	aliceAddr, _ = net.ResolveTCPAddr("tcp", "10.0.0.3:9000")

	defaultFeeRate = chainfee.AtomPerKByte(1e4)
)

// assertProperBalance asserts than the total value of the unspent outputs
// within the wallet are *exactly* amount. If unable to retrieve the current
// balance, or the assertion fails, the test will halt with a fatal error.
func assertProperBalance(t *testing.T, lw *lnwallet.LightningWallet,
	numConfirms int32, amount float64) {

	balance, err := lw.ConfirmedBalance(numConfirms)
	if err != nil {
		t.Fatalf("unable to query for balance: %v", err)
	}
	if balance.ToCoin() != amount {
		t.Fatalf("wallet credits not properly loaded, should have %v DCR, "+
			"instead have %v", amount, balance)
	}
}

func assertReservationDeleted(res *lnwallet.ChannelReservation, t *testing.T) {
	if err := res.Cancel(); err == nil {
		t.Fatalf("reservation wasn't deleted from wallet")
	}
}

// mineAndAssertTxInBlock asserts that a transaction is included within the next
// block mined.
func mineAndAssertTxInBlock(t *testing.T, miner *rpctest.Harness,
	vw *rpctest.VotingWallet, txid chainhash.Hash) {

	t.Helper()

	// First, we'll wait for the transaction to arrive in the mempool.
	if err := waitForMempoolTx(miner, &txid); err != nil {
		t.Fatalf("unable to find %v in the mempool: %v", txid, err)
	}

	// We'll mined a block to confirm it.
	blockHashes, err := vw.GenerateBlocks(context.TODO(), 1)
	if err != nil {
		t.Fatalf("unable to generate new block: %v", err)
	}

	// Finally, we'll check it was actually mined in this block.
	block, err := miner.Node.GetBlock(context.TODO(), blockHashes[0])
	if err != nil {
		t.Fatalf("unable to get block %v: %v", blockHashes[0], err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("expected 2 transactions in block, found %d",
			len(block.Transactions))
	}
	txHash := block.Transactions[1].TxHash()
	if txHash != txid {
		t.Fatalf("expected transaction %v to be mined, found %v", txid,
			txHash)
	}
}

// newPkScript generates a new public key script of the given address type.
func newPkScript(t *testing.T, w *lnwallet.LightningWallet,
	addrType lnwallet.AddressType) []byte {

	t.Helper()

	addr, err := w.NewAddress(addrType, false)
	if err != nil {
		t.Fatalf("unable to create new address: %v", err)
	}
	pkScript, err := input.PayToAddrScript(addr)
	if err != nil {
		t.Fatalf("unable to create output script: %v", err)
	}

	// Wait for the wallet's address manager to close (see dcrwallet#1372).
	time.Sleep(time.Millisecond * 50)

	return pkScript
}

// sendCoins is a helper function that encompasses all the things needed for two
// parties to send on-chain funds to each other.
func sendCoins(t *testing.T, miner *rpctest.Harness, vw *rpctest.VotingWallet,
	sender, receiver *lnwallet.LightningWallet, output *wire.TxOut,
	feeRate chainfee.AtomPerKByte) *wire.MsgTx {

	t.Helper()

	tx, err := sender.SendOutputs(
		[]*wire.TxOut{output}, feeRate, labels.External,
	)
	if err != nil {
		t.Fatalf("unable to send transaction: %v", err)
	}

	mineAndAssertTxInBlock(t, miner, vw, tx.TxHash())

	if err := waitForWalletSync(miner, sender); err != nil {
		t.Fatalf("unable to sync alice: %v", err)
	}
	if err := waitForWalletSync(miner, receiver); err != nil {
		t.Fatalf("unable to sync bob: %v", err)
	}

	return tx
}

// assertTxInWallet asserts that a transaction exists in the wallet with the
// expected confirmation status.
func assertTxInWallet(t *testing.T, w *lnwallet.LightningWallet,
	txHash chainhash.Hash, confirmed bool) {

	t.Helper()

	// If the backend is Neutrino, then we can't determine unconfirmed
	// transactions since it's not aware of the mempool.
	if !confirmed && w.BackEnd() == "neutrino" {
		return
	}

	// We'll fetch all of our transaction and go through each one until
	// finding the expected transaction with its expected confirmation
	// status.
	txs, err := w.ListTransactionDetails(0, dcrwallet.UnconfirmedHeight)
	if err != nil {
		t.Fatalf("unable to retrieve transactions: %v", err)
	}
	for _, tx := range txs {
		if tx.Hash != txHash {
			continue
		}
		if tx.NumConfirmations <= 0 && confirmed {
			t.Fatalf("expected transaction %v to be confirmed",
				txHash)
		}
		if tx.NumConfirmations > 0 && !confirmed {
			t.Fatalf("expected transaction %v to be unconfirmed",
				txHash)
		}

		// We've found the transaction and it matches the desired
		// confirmation status, so we can exit.
		return
	}

	t.Fatalf("transaction %v not found", txHash)
}

func loadTestCredits(miner *rpctest.Harness, w *lnwallet.LightningWallet,
	blockGenerator func(context.Context, uint32) ([]*chainhash.Hash, error),
	numOutputs int, dcrPerOutput float64) error {

	// Using the mining node, spend from a coinbase output numOutputs to
	// give us dcrPerOutput with each output.
	atomsPerOutput, err := dcrutil.NewAmount(dcrPerOutput)
	if err != nil {
		return fmt.Errorf("unable to create amt: %v", err)
	}
	expectedBalance, err := w.ConfirmedBalance(1)
	if err != nil {
		return err
	}
	expectedBalance += dcrutil.Amount(int64(atomsPerOutput) * int64(numOutputs))
	addrs := make([]stdaddr.Address, numOutputs)
	for i := 0; i < numOutputs; i++ {
		// Grab a fresh address from the wallet to house this output.
		addrs[i], err = w.NewAddress(lnwallet.PubKeyHash, false)
		if err != nil {
			return err
		}
	}

	// Sleep for a bit to allow the wallet to unlock the mutexes of the address
	// manager and underlying database. This is needed to prevent a possible
	// deadlock condition in the wallet when generating new addresses while
	// processing a transaction (see
	// https://github.com/dcrwallet/issues/1372). This isn't pretty,
	// but 200ms should be more than enough to prevent triggering this bug
	// on most dev machines.
	time.Sleep(time.Millisecond * 200)

	for _, walletAddr := range addrs {
		script, err := input.PayToAddrScript(walletAddr)
		if err != nil {
			return err
		}

		output := &wire.TxOut{
			Value:    int64(atomsPerOutput),
			PkScript: script,
			Version:  scriptVersion,
		}
		if _, err := miner.SendOutputs([]*wire.TxOut{output}, 1e5); err != nil {
			return err
		}
	}

	// TODO(roasbeef): shouldn't hardcode 10, use config param that dictates
	// how many confs we wait before opening a channel.
	// Generate 10 blocks with the mining node, this should mine all
	// numOutputs transactions created above. We generate 10 blocks here
	// in order to give all the outputs a "sufficient" number of confirmations.
	if _, err := blockGenerator(context.TODO(), 10); err != nil {
		return err
	}

	time.Sleep(time.Millisecond * 200)

	// Wait until the wallet has finished syncing up to the main chain.
	ticker := time.NewTicker(100 * time.Millisecond)
	timeout := time.After(30 * time.Second)

	for range ticker.C {
		balance, err := w.ConfirmedBalance(1)
		if err != nil {
			return err
		}
		if balance == expectedBalance {
			break
		}
		select {
		case <-timeout:
			synced, _, err := w.IsSynced()
			if err != nil {
				return err
			}
			return fmt.Errorf("timed out after 30 seconds "+
				"waiting for balance %v, current balance %v, "+
				"synced: %t", expectedBalance, balance, synced)
		default:
		}
	}
	ticker.Stop()

	return nil
}

// createTestWallet creates a test LightningWallet will a total of 20DCR
// available for funding channels.
func createTestWallet(cdb *channeldb.DB, miningNode *rpctest.Harness,
	netParams *chaincfg.Params, notifier chainntnfs.ChainNotifier,
	wc lnwallet.WalletController, keyRing keychain.SecretKeyRing,
	signer input.Signer, bio lnwallet.BlockChainIO,
	vw *rpctest.VotingWallet) (*lnwallet.LightningWallet, error) {

	cfg := lnwallet.Config{
		Database:         cdb,
		Notifier:         notifier,
		SecretKeyRing:    keyRing,
		WalletController: wc,
		Signer:           signer,
		ChainIO:          bio,
		FeeEstimator:     chainfee.NewStaticEstimator(defaultFeeRate, 0),
		DefaultConstraints: channeldb.ChannelConstraints{
			DustLimit:        500,
			MaxPendingAmount: lnwire.NewMAtomsFromAtoms(dcrutil.AtomsPerCoin) * 100,
			ChanReserve:      100,
			MinHTLC:          400,
			MaxAcceptedHtlcs: 900,
		},
		NetParams: *netParams,
	}

	wallet, err := lnwallet.NewLightningWallet(cfg)
	if err != nil {
		return nil, err
	}

	if err := wallet.Startup(); err != nil {
		return nil, err
	}

	return wallet, nil
}

func testGetRecoveryInfo(miner *rpctest.Harness, // nolint: unused
	vw *rpctest.VotingWallet, alice, bob *lnwallet.LightningWallet,
	t *testing.T) {

	// alice's wallet is in recovery mode
	expectedRecoveryMode := true
	expectedProgress := float64(1)

	isRecoveryMode, progress, err := alice.GetRecoveryInfo()
	require.NoError(t, err, "unable to get alice's recovery info")

	require.Equal(t,
		expectedRecoveryMode, isRecoveryMode, "recovery mode incorrect",
	)
	require.Equal(t, expectedProgress, progress, "progress incorrect")

	// Generate 5 blocks and check the recovery process again.
	const numBlocksMined = 5
	_, err = vw.GenerateBlocks(context.TODO(), numBlocksMined)
	require.NoError(t, err, "unable to mine blocks")

	// Check the recovery process. Once synced, the progress should be 1.
	err = waitForWalletSync(miner, alice)
	require.NoError(t, err, "Couldn't sync Alice's wallet")

	isRecoveryMode, progress, err = alice.GetRecoveryInfo()
	require.NoError(t, err, "unable to get alice's recovery info")

	require.Equal(t,
		expectedRecoveryMode, isRecoveryMode, "recovery mode incorrect",
	)
	require.Equal(t, expectedProgress, progress, "progress incorrect")

	// bob's wallet is not in recovery mode
	expectedRecoveryMode = false
	expectedProgress = float64(0)

	isRecoveryMode, progress, err = bob.GetRecoveryInfo()
	require.NoError(t, err, "unable to get bob's recovery info")

	require.Equal(t,
		expectedRecoveryMode, isRecoveryMode, "recovery mode incorrect",
	)
	require.Equal(t, expectedProgress, progress, "progress incorrect")
}

func testDualFundingReservationWorkflow(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, bob *lnwallet.LightningWallet,
	t *testing.T) {

	fundingAmount, err := dcrutil.NewAmount(5)
	if err != nil {
		t.Fatalf("unable to create amt: %v", err)
	}

	// In this scenario, we'll test a dual funder reservation, with each
	// side putting in 10 DCR.

	// Alice initiates a channel funded with 5 DCR for each side, so 10 DCR
	// total. She also generates 2 DCR in change.
	feePerKB, err := alice.Cfg.FeeEstimator.EstimateFeePerKB(1)
	if err != nil {
		t.Fatalf("unable to query fee estimator: %v", err)
	}

	aliceReq := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  fundingAmount,
		RemoteFundingAmt: fundingAmount,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
	}
	aliceChanReservation, err := alice.InitChannelReservation(aliceReq)
	if err != nil {
		t.Fatalf("unable to initialize funding reservation: %v", err)
	}
	aliceChanReservation.SetNumConfsRequired(numReqConfs)
	channelConstraints := &channeldb.ChannelConstraints{
		DustLimit:        lnwallet.DefaultDustLimit(),
		ChanReserve:      fundingAmount / 100,
		MaxPendingAmount: lnwire.NewMAtomsFromAtoms(fundingAmount),
		MinHTLC:          1,
		MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
		CsvDelay:         csvDelay,
	}
	err = aliceChanReservation.CommitConstraints(channelConstraints)
	if err != nil {
		t.Fatalf("unable to verify constraints: %v", err)
	}

	// The channel reservation should now be populated with a multi-sig key
	// from our HD chain, a change output with 3 DCR, and 2 outputs
	// selected of 4 DCR each. Additionally, the rest of the items needed
	// to fulfill a funding contribution should also have been filled in.
	aliceContribution := aliceChanReservation.OurContribution()
	if len(aliceContribution.Inputs) < 1 {
		t.Fatalf("outputs for funding tx not properly selected, have %v "+
			"outputs should at least 1", len(aliceContribution.Inputs))
	}
	if len(aliceContribution.ChangeOutputs) != 1 {
		t.Fatalf("coin selection failed, should have one change outputs, "+
			"instead have: %v", len(aliceContribution.ChangeOutputs))
	}
	assertContributionInitPopulated(t, aliceContribution)

	// Bob does the same, generating his own contribution. He then also
	// receives' Alice's contribution, and consumes that so we can continue
	// the funding process.
	bobReq := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           alicePub,
		NodeAddr:         aliceAddr,
		LocalFundingAmt:  fundingAmount,
		RemoteFundingAmt: fundingAmount,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
	}
	bobChanReservation, err := bob.InitChannelReservation(bobReq)
	if err != nil {
		t.Fatalf("bob unable to init channel reservation: %v", err)
	}
	err = bobChanReservation.CommitConstraints(channelConstraints)
	if err != nil {
		t.Fatalf("unable to verify constraints: %v", err)
	}
	bobChanReservation.SetNumConfsRequired(numReqConfs)

	assertContributionInitPopulated(t, bobChanReservation.OurContribution())

	err = bobChanReservation.ProcessContribution(aliceContribution)
	if err != nil {
		t.Fatalf("bob unable to process alice's contribution: %v", err)
	}
	assertContributionInitPopulated(t, bobChanReservation.TheirContribution())

	bobContribution := bobChanReservation.OurContribution()

	// Bob then sends over his contribution, which will be consumed by
	// Alice. After this phase, Alice should have all the necessary
	// material required to craft the funding transaction and commitment
	// transactions.
	err = aliceChanReservation.ProcessContribution(bobContribution)
	if err != nil {
		t.Fatalf("alice unable to process bob's contribution: %v", err)
	}
	assertContributionInitPopulated(t, aliceChanReservation.TheirContribution())

	// At this point, all Alice's signatures should be fully populated.
	aliceFundingSigs, aliceCommitSig := aliceChanReservation.OurSignatures()
	if aliceFundingSigs == nil {
		t.Fatalf("alice's funding signatures not populated")
	}
	if aliceCommitSig == nil {
		t.Fatalf("alice's commit signatures not populated")
	}

	// Additionally, Bob's signatures should also be fully populated.
	bobFundingSigs, bobCommitSig := bobChanReservation.OurSignatures()
	if bobFundingSigs == nil {
		t.Fatalf("bob's funding signatures not populated")
	}
	if bobCommitSig == nil {
		t.Fatalf("bob's commit signatures not populated")
	}

	// To conclude, we'll consume first Alice's signatures with Bob, and
	// then the other way around.
	_, err = aliceChanReservation.CompleteReservation(
		bobFundingSigs, bobCommitSig,
	)
	if err != nil {
		for _, in := range aliceChanReservation.FinalFundingTx().TxIn {
			fmt.Println(in.PreviousOutPoint.String())
		}
		t.Fatalf("unable to consume alice's sigs: %v", err)
	}
	_, err = bobChanReservation.CompleteReservation(
		aliceFundingSigs, aliceCommitSig,
	)
	if err != nil {
		t.Fatalf("unable to consume bob's sigs: %v", err)
	}

	// At this point, the funding tx should have been populated.
	fundingTx := aliceChanReservation.FinalFundingTx()
	if fundingTx == nil {
		t.Fatalf("funding transaction never created!")
	}

	// The resulting active channel state should have been persisted to the
	// DB.
	fundingSha := fundingTx.TxHash()
	aliceChannels, err := alice.Cfg.Database.FetchOpenChannels(bobPub)
	if err != nil {
		t.Fatalf("unable to retrieve channel from DB: %v", err)
	}
	if !bytes.Equal(aliceChannels[0].FundingOutpoint.Hash[:], fundingSha[:]) {
		t.Fatalf("channel state not properly saved")
	}
	if !aliceChannels[0].ChanType.IsDualFunder() {
		t.Fatalf("channel not detected as dual funder")
	}
	bobChannels, err := bob.Cfg.Database.FetchOpenChannels(alicePub)
	if err != nil {
		t.Fatalf("unable to retrieve channel from DB: %v", err)
	}
	if !bytes.Equal(bobChannels[0].FundingOutpoint.Hash[:], fundingSha[:]) {
		t.Fatalf("channel state not properly saved")
	}
	if !bobChannels[0].ChanType.IsDualFunder() {
		t.Fatalf("channel not detected as dual funder")
	}

	// Let Alice publish the funding transaction.
	err = alice.PublishTransaction(fundingTx, "")
	if err != nil {
		t.Fatalf("unable to publish funding tx: %v", err)
	}

	// Mine a single block, the funding transaction should be included
	// within this block.
	err = waitForMempoolTx(miner, &fundingSha)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}
	blockHashes, err := vw.GenerateBlocks(context.TODO(), 1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}
	block, err := miner.Node.GetBlock(context.TODO(), blockHashes[0])
	if err != nil {
		t.Fatalf("unable to find block: %v", err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("funding transaction wasn't mined: %v", err)
	}
	blockTx := block.Transactions[1]
	if blockTx.TxHash() != fundingSha {
		t.Fatalf("incorrect transaction was mined")
	}

	assertReservationDeleted(aliceChanReservation, t)
	assertReservationDeleted(bobChanReservation, t)

	// Wait for wallets to catch up to prevent issues in subsequent tests.
	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("unable to sync alice: %v", err)
	}
	err = waitForWalletSync(miner, bob)
	if err != nil {
		t.Fatalf("unable to sync bob: %v", err)
	}
}

func testFundingTransactionLockedOutputs(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// Create a single channel asking for 16 DCR total.
	fundingAmount := dcrutil.Amount(8 * 1e8)

	feePerKB, err := alice.Cfg.FeeEstimator.EstimateFeePerKB(1)
	if err != nil {
		t.Fatalf("unable to query fee estimator: %v", err)
	}
	req := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  fundingAmount,
		RemoteFundingAmt: 0,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
		PendingChanID:    [32]byte{0, 1, 2, 3},
	}
	chanReservation, err := alice.InitChannelReservation(req)
	if err != nil {
		t.Fatalf("unable to initialize funding reservation 1: %v", err)
	}

	// Now attempt to reserve funds for another channel, this time
	// requesting 900 DCR. We only have around 64DCR worth of outpoints
	// that aren't locked, so this should fail.
	amt, err := dcrutil.NewAmount(900)
	if err != nil {
		t.Fatalf("unable to create amt: %v", err)
	}
	failedReq := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  amt,
		RemoteFundingAmt: 0,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
		PendingChanID:    [32]byte{1, 2, 3, 4},
	}
	failedReservation, err := alice.InitChannelReservation(failedReq)
	if err == nil {
		t.Fatalf("not error returned, should fail on coin selection")
	}
	if _, ok := err.(*chanfunding.ErrInsufficientFunds); !ok {
		t.Fatalf("error not coinselect error: %v", err)
	}
	if failedReservation != nil {
		t.Fatalf("reservation should be nil")
	}

	// Cancel the older reservation so it won't affect other tests.
	if err := chanReservation.Cancel(); err != nil {
		t.Fatalf("unable to cancel reservation: %v", err)
	}
}

func testFundingCancellationNotEnoughFunds(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	feePerKB, err := alice.Cfg.FeeEstimator.EstimateFeePerKB(1)
	if err != nil {
		t.Fatalf("unable to query fee estimator: %v", err)
	}

	totalBalance, err := alice.ConfirmedBalance(0)
	if err != nil {
		t.Fatalf("unable to fetch confirmed balance: %v", err)
	}

	// Create a reservation for almost the entire wallet amount.
	fundingAmount := totalBalance - dcrutil.AtomsPerCoin
	req := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  fundingAmount,
		RemoteFundingAmt: 0,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
		PendingChanID:    [32]byte{2, 3, 4, 5},
	}
	chanReservation, err := alice.InitChannelReservation(req)
	if err != nil {
		t.Fatalf("unable to initialize funding reservation: %v", err)
	}

	// Attempt to create another channel spending the same amount. This
	// should fail.
	req.PendingChanID = [32]byte{3, 4, 5, 6}
	_, err = alice.InitChannelReservation(req)
	if _, ok := err.(*chanfunding.ErrInsufficientFunds); !ok {
		t.Fatalf("coin selection succeeded should have insufficient funds: %v",
			err)
	}

	// Now cancel that old reservation.
	if err := chanReservation.Cancel(); err != nil {
		t.Fatalf("unable to cancel reservation: %v", err)
	}

	// Those outpoints should no longer be locked.
	lockedOutPoints := alice.LockedOutpoints()
	if len(lockedOutPoints) != 0 {
		t.Fatalf("outpoints still locked")
	}

	// Reservation ID should no longer be tracked.
	numReservations := alice.ActiveReservations()
	if len(alice.ActiveReservations()) != 0 {
		t.Fatalf("should have 0 reservations, instead have %v",
			numReservations)
	}

	// TODO(roasbeef): create method like Balance that ignores locked
	// outpoints, will let us fail early/fast instead of querying and
	// attempting coin selection.

	// Request to fund a new channel should now succeed.
	req.PendingChanID = [32]byte{4, 5, 6, 7, 8}
	if _, err := alice.InitChannelReservation(req); err != nil {
		t.Fatalf("unable to initialize funding reservation: %v", err)
	}
}

func testCancelNonExistentReservation(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	feePerKB, err := alice.Cfg.FeeEstimator.EstimateFeePerKB(1)
	if err != nil {
		t.Fatalf("unable to query fee estimator: %v", err)
	}

	// Create our own reservation, give it some ID.
	res, err := lnwallet.NewChannelReservation(
		20000, 20000, feePerKB, alice, 22, 10, &testHdSeed,
		lnwire.FFAnnounceChannel, lnwallet.CommitmentTypeTweakless,
		nil, [32]byte{}, 0,
	)
	if err != nil {
		t.Fatalf("unable to create res: %v", err)
	}

	// Attempt to cancel this reservation. This should fail, we know
	// nothing of it.
	if err := res.Cancel(); err == nil {
		t.Fatalf("canceled non-existent reservation")
	}
}

func testReservationInitiatorBalanceBelowDustCancel(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// We'll attempt to create a new reservation with an extremely high
	// commitment fee rate. This should push our balance into the negative
	// and result in a failure to create the reservation.
	const numDCR = 4
	fundingAmount, err := dcrutil.NewAmount(numDCR)
	if err != nil {
		t.Fatalf("unable to create amt: %v", err)
	}

	FeePerKB := chainfee.AtomPerKByte(
		numDCR * numDCR * dcrutil.AtomsPerCoin,
	)
	req := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  fundingAmount,
		RemoteFundingAmt: 0,
		CommitFeePerKB:   FeePerKB,
		FundingFeePerKB:  1000,
		PushMAtoms:       0,
		Flags:            lnwire.FFAnnounceChannel,
		CommitType:       lnwallet.CommitmentTypeTweakless,
	}
	_, err = alice.InitChannelReservation(req)
	switch {
	case err == nil:
		t.Fatalf("initialization should have failed due to " +
			"insufficient local amount")

	case !strings.Contains(err.Error(), "funder balance too small"):
		t.Fatalf("incorrect error: %v", err)
	}
}

func assertContributionInitPopulated(t *testing.T, c *lnwallet.ChannelContribution) {
	_, _, line, _ := runtime.Caller(1)

	if c.FirstCommitmentPoint == nil {
		t.Fatalf("line #%v: commitment point not fond", line)
	}

	if c.CsvDelay == 0 {
		t.Fatalf("line #%v: csv delay not set", line)
	}

	if c.MultiSigKey.PubKey == nil {
		t.Fatalf("line #%v: multi-sig key not set", line)
	}
	if c.RevocationBasePoint.PubKey == nil {
		t.Fatalf("line #%v: revocation key not set", line)
	}
	if c.PaymentBasePoint.PubKey == nil {
		t.Fatalf("line #%v: payment key not set", line)
	}
	if c.DelayBasePoint.PubKey == nil {
		t.Fatalf("line #%v: delay key not set", line)
	}

	if c.DustLimit == 0 {
		t.Fatalf("line #%v: dust limit not set", line)
	}
	if c.MaxPendingAmount == 0 {
		t.Fatalf("line #%v: max pending amt not set", line)
	}
	if c.ChanReserve == 0 {
		t.Fatalf("line #%v: chan reserve not set", line)
	}
	if c.MinHTLC == 0 {
		t.Fatalf("line #%v: min htlc not set", line)
	}
	if c.MaxAcceptedHtlcs == 0 {
		t.Fatalf("line #%v: max accepted htlc's not set", line)
	}
}

func testSingleFunderReservationWorkflow(miner *rpctest.Harness,
	vw *rpctest.VotingWallet,
	alice, bob *lnwallet.LightningWallet, t *testing.T,
	commitType lnwallet.CommitmentType,
	aliceChanFunder chanfunding.Assembler, fetchFundingTx func() *wire.MsgTx,
	pendingChanID [32]byte, thawHeight uint32) {

	// For this scenario, Alice will be the channel initiator while bob
	// will act as the responder to the workflow.

	// First, Alice will Initialize a reservation for a channel with 4 DCR
	// funded solely by us. We'll also initially push 1 DCR of the channel
	// towards Bob's side.
	fundingAmt, err := dcrutil.NewAmount(4)
	if err != nil {
		t.Fatalf("unable to create amt: %v", err)
	}
	pushAmt := lnwire.NewMAtomsFromAtoms(dcrutil.AtomsPerCoin)
	feePerKB, err := alice.Cfg.FeeEstimator.EstimateFeePerKB(1)
	if err != nil {
		t.Fatalf("unable to query fee estimator: %v", err)
	}
	aliceReq := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		PendingChanID:    pendingChanID,
		NodeID:           bobPub,
		NodeAddr:         bobAddr,
		LocalFundingAmt:  fundingAmt,
		RemoteFundingAmt: 0,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       pushAmt,
		Flags:            lnwire.FFAnnounceChannel,
		CommitType:       commitType,
		ChanFunder:       aliceChanFunder,
	}
	aliceChanReservation, err := alice.InitChannelReservation(aliceReq)
	if err != nil {
		t.Fatalf("unable to init channel reservation: %v", err)
	}
	aliceChanReservation.SetNumConfsRequired(numReqConfs)
	channelConstraints := &channeldb.ChannelConstraints{
		DustLimit:        lnwallet.DefaultDustLimit(),
		ChanReserve:      fundingAmt / 100,
		MaxPendingAmount: lnwire.NewMAtomsFromAtoms(fundingAmt),
		MinHTLC:          1,
		MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
		CsvDelay:         csvDelay,
	}
	err = aliceChanReservation.CommitConstraints(channelConstraints)
	if err != nil {
		t.Fatalf("unable to verify constraints: %v", err)
	}

	// Verify all contribution fields have been set properly, but only if
	// Alice is the funder herself.
	aliceContribution := aliceChanReservation.OurContribution()
	if fetchFundingTx == nil {
		if len(aliceContribution.Inputs) < 1 {
			t.Fatalf("outputs for funding tx not properly "+
				"selected, have %v outputs should at least 1",
				len(aliceContribution.Inputs))
		}
		if len(aliceContribution.ChangeOutputs) != 1 {
			t.Fatalf("coin selection failed, should have one "+
				"change outputs, instead have: %v",
				len(aliceContribution.ChangeOutputs))
		}
	}
	assertContributionInitPopulated(t, aliceContribution)

	// Next, Bob receives the initial request, generates a corresponding
	// reservation initiation, then consume Alice's contribution.
	bobReq := &lnwallet.InitFundingReserveMsg{
		ChainHash:        chainHash,
		PendingChanID:    pendingChanID,
		NodeID:           alicePub,
		NodeAddr:         aliceAddr,
		LocalFundingAmt:  0,
		RemoteFundingAmt: fundingAmt,
		CommitFeePerKB:   feePerKB,
		FundingFeePerKB:  feePerKB,
		PushMAtoms:       pushAmt,
		Flags:            lnwire.FFAnnounceChannel,
		CommitType:       commitType,
	}
	bobChanReservation, err := bob.InitChannelReservation(bobReq)
	if err != nil {
		t.Fatalf("unable to create bob reservation: %v", err)
	}
	err = bobChanReservation.CommitConstraints(channelConstraints)
	if err != nil {
		t.Fatalf("unable to verify constraints: %v", err)
	}
	bobChanReservation.SetNumConfsRequired(numReqConfs)

	// We'll ensure that Bob's contribution also gets generated properly.
	bobContribution := bobChanReservation.OurContribution()
	assertContributionInitPopulated(t, bobContribution)

	// With his contribution generated, he can now process Alice's
	// contribution.
	err = bobChanReservation.ProcessSingleContribution(aliceContribution)
	if err != nil {
		t.Fatalf("bob unable to process alice's contribution: %v", err)
	}
	assertContributionInitPopulated(t, bobChanReservation.TheirContribution())

	// Bob will next send over his contribution to Alice, we simulate this
	// by having Alice immediately process his contribution.
	err = aliceChanReservation.ProcessContribution(bobContribution)
	if err != nil {
		t.Fatalf("alice unable to process bob's contribution: %v", err)
	}
	assertContributionInitPopulated(t, bobChanReservation.TheirContribution())

	// At this point, Alice should have generated all the signatures
	// required for the funding transaction, as well as Alice's commitment
	// signature to bob, but only if the funding transaction was
	// constructed internally.
	aliceRemoteContribution := aliceChanReservation.TheirContribution()
	aliceFundingSigs, aliceCommitSig := aliceChanReservation.OurSignatures()
	if fetchFundingTx == nil && aliceFundingSigs == nil {
		t.Fatalf("funding sigs not found")
	}
	if aliceCommitSig == nil {
		t.Fatalf("commitment sig not found")
	}

	// Additionally, the funding tx and the funding outpoint should have
	// been populated.
	if aliceChanReservation.FinalFundingTx() == nil && fetchFundingTx == nil {
		t.Fatalf("funding transaction never created!")
	}
	if aliceChanReservation.FundingOutpoint() == nil {
		t.Fatalf("funding outpoint never created!")
	}

	// Their funds should also be filled in.
	if len(aliceRemoteContribution.Inputs) != 0 {
		t.Fatalf("bob shouldn't have any inputs, instead has %v",
			len(aliceRemoteContribution.Inputs))
	}
	if len(aliceRemoteContribution.ChangeOutputs) != 0 {
		t.Fatalf("bob shouldn't have any change outputs, instead "+
			"has %v",
			aliceRemoteContribution.ChangeOutputs[0].Value)
	}

	// Next, Alice will send over her signature for Bob's commitment
	// transaction, as well as the funding outpoint.
	fundingPoint := aliceChanReservation.FundingOutpoint()
	_, err = bobChanReservation.CompleteReservationSingle(
		fundingPoint, aliceCommitSig,
	)
	if err != nil {
		t.Fatalf("bob unable to consume single reservation: %v", err)
	}

	// Finally, we'll conclude the reservation process by sending over
	// Bob's commitment signature, which is the final thing Alice needs to
	// be able to safely broadcast the funding transaction.
	_, bobCommitSig := bobChanReservation.OurSignatures()
	if bobCommitSig == nil {
		t.Fatalf("bob failed to generate commitment signature: %v", err)
	}
	_, err = aliceChanReservation.CompleteReservation(
		nil, bobCommitSig,
	)
	if err != nil {
		t.Fatalf("alice unable to complete reservation: %v", err)
	}

	// If the caller provided an alternative way to obtain the funding tx,
	// then we'll use that. Otherwise, we'll obtain it directly from Alice.
	var fundingTx *wire.MsgTx
	if fetchFundingTx != nil {
		fundingTx = fetchFundingTx()
	} else {
		fundingTx = aliceChanReservation.FinalFundingTx()
	}

	// The resulting active channel state should have been persisted to the
	// DB for both Alice and Bob.
	fundingSha := fundingTx.TxHash()
	aliceChannels, err := alice.Cfg.Database.FetchOpenChannels(bobPub)
	if err != nil {
		t.Fatalf("unable to retrieve channel from DB: %v", err)
	}
	if len(aliceChannels) != 1 {
		t.Fatalf("alice didn't save channel state: %v", err)
	}
	if !bytes.Equal(aliceChannels[0].FundingOutpoint.Hash[:], fundingSha[:]) {
		t.Fatalf("channel state not properly saved: %v vs %v",
			hex.EncodeToString(aliceChannels[0].FundingOutpoint.Hash[:]),
			hex.EncodeToString(fundingSha[:]))
	}
	if !aliceChannels[0].IsInitiator {
		t.Fatalf("alice not detected as channel initiator")
	}
	if !aliceChannels[0].ChanType.IsSingleFunder() {
		t.Fatalf("channel type is incorrect, expected %v instead got %v",
			channeldb.SingleFunderBit, aliceChannels[0].ChanType)
	}

	bobChannels, err := bob.Cfg.Database.FetchOpenChannels(alicePub)
	if err != nil {
		t.Fatalf("unable to retrieve channel from DB: %v", err)
	}
	if len(bobChannels) != 1 {
		t.Fatalf("bob didn't save channel state: %v", err)
	}
	if !bytes.Equal(bobChannels[0].FundingOutpoint.Hash[:], fundingSha[:]) {
		t.Fatalf("channel state not properly saved: %v vs %v",
			hex.EncodeToString(bobChannels[0].FundingOutpoint.Hash[:]),
			hex.EncodeToString(fundingSha[:]))
	}
	if bobChannels[0].IsInitiator {
		t.Fatalf("bob not detected as channel responder")
	}
	if !bobChannels[0].ChanType.IsSingleFunder() {
		t.Fatalf("channel type is incorrect, expected %v instead got %v",
			channeldb.SingleFunderBit, bobChannels[0].ChanType)
	}

	// Let Alice publish the funding transaction.
	err = alice.PublishTransaction(fundingTx, "")
	if err != nil {
		t.Fatalf("unable to publish funding tx: %v", err)
	}

	// Mine a single block, the funding transaction should be included
	// within this block.
	err = waitForMempoolTx(miner, &fundingSha)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}
	blockHashes, err := vw.GenerateBlocks(context.TODO(), 1)
	if err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}
	block, err := miner.Node.GetBlock(context.TODO(), blockHashes[0])
	if err != nil {
		t.Fatalf("unable to find block: %v", err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("funding transaction wasn't mined: %d",
			len(block.Transactions))
	}
	blockTx := block.Transactions[1]
	if blockTx.TxHash() != fundingSha {
		t.Fatalf("incorrect transaction was mined")
	}

	// If a frozen channel was requested, then we expect that both channel
	// types show as being a frozen channel type.
	aliceChanFrozen := aliceChannels[0].ChanType.IsFrozen()
	bobChanFrozen := bobChannels[0].ChanType.IsFrozen()
	if thawHeight != 0 && (!aliceChanFrozen || !bobChanFrozen) {
		t.Fatalf("expected both alice and bob to have frozen chans: "+
			"alice_frozen=%v, bob_frozen=%v", aliceChanFrozen,
			bobChanFrozen)
	}
	if thawHeight != bobChannels[0].ThawHeight {
		t.Fatalf("wrong thaw height: expected %v got %v", thawHeight,
			bobChannels[0].ThawHeight)
	}
	if thawHeight != aliceChannels[0].ThawHeight {
		t.Fatalf("wrong thaw height: expected %v got %v", thawHeight,
			aliceChannels[0].ThawHeight)
	}

	assertReservationDeleted(aliceChanReservation, t)
	assertReservationDeleted(bobChanReservation, t)
}

func testListTransactionDetails(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// Create 5 new outputs spendable by the wallet.
	const numTxns = 5
	const outputAmt = dcrutil.AtomsPerCoin
	var err error
	addrs := make([]stdaddr.Address, numTxns)
	for i := 0; i < numTxns; i++ {
		addrs[i], err = alice.NewAddress(lnwallet.PubKeyHash, false)
		if err != nil {
			t.Fatalf("unable to create new address: %v", err)
		}
	}

	// Let the wallet close the address manager (see issue dcrwallet#1372).
	time.Sleep(time.Millisecond * 50)

	txids := make(map[chainhash.Hash]struct{})
	for i := 0; i < numTxns; i++ {
		script, err := input.PayToAddrScript(addrs[i])
		if err != nil {
			t.Fatalf("unable to create output script: %v", err)
		}

		output := &wire.TxOut{
			Value:    outputAmt,
			PkScript: script,
		}
		txid, err := miner.SendOutputs([]*wire.TxOut{output},
			dcrutil.Amount(defaultFeeRate))
		if err != nil {
			t.Fatalf("unable to send coinbase: %v", err)
		}
		txids[*txid] = struct{}{}
		err = waitForMempoolTx(miner, txid)
		if err != nil {
			t.Fatalf("unable to detect mempool tx: %v", err)
		}
	}

	// Get the miner's current best block height before we mine blocks.
	_, startHeight, err := miner.Node.GetBestBlock(context.TODO())
	if err != nil {
		t.Fatalf("cannot get best block: %v", err)
	}

	// Generate 10 blocks to mine all the transactions created above.
	const numBlocksMined = 10
	blocks, err := vw.GenerateBlocks(context.TODO(), numBlocksMined)
	if err != nil {
		t.Fatalf("unable to mine blocks: %v", err)
	}

	// Our new best block height should be our start height + the number of
	// blocks we just mined.
	chainTip := startHeight + numBlocksMined

	// Next, fetch all the current transaction details. We should find all
	// of our transactions between our start height before we generated
	// blocks, and our end height, which is the chain tip. This query does
	// not include unconfirmed transactions, since all of our transactions
	// should be confirmed.
	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}
	txDetails, err := alice.ListTransactionDetails(
		int32(startHeight), int32(chainTip),
	)
	if err != nil {
		t.Fatalf("unable to fetch tx details: %v", err)
	}

	// This is a mapping from:
	// blockHash -> transactionHash -> transactionOutputs
	blockTxOuts := make(map[chainhash.Hash]map[chainhash.Hash][]*wire.TxOut)

	// Each of the transactions created above should be found with the
	// proper details populated.
	for _, txDetail := range txDetails {
		if _, ok := txids[txDetail.Hash]; !ok {
			continue
		}

		if txDetail.NumConfirmations != numBlocksMined {
			t.Fatalf("num confs incorrect, got %v expected %v",
				txDetail.NumConfirmations, numBlocksMined)
		}
		if txDetail.Value != outputAmt {
			t.Fatalf("tx value incorrect, got %v expected %v",
				txDetail.Value, outputAmt)
		}

		if !bytes.Equal(txDetail.BlockHash[:], blocks[0][:]) {
			t.Fatalf("block hash mismatch, got %v expected %v",
				txDetail.BlockHash, blocks[0])
		}

		// This fetches the transactions in a block so that we can compare the
		// txouts stored in the mined transaction against the ones in the transaction
		// details
		if _, ok := blockTxOuts[*txDetail.BlockHash]; !ok {
			fetchedBlock, err := alice.Cfg.ChainIO.GetBlock(txDetail.BlockHash)
			if err != nil {
				t.Fatalf("err fetching block: %s", err)
			}

			transactions :=
				make(map[chainhash.Hash][]*wire.TxOut, len(fetchedBlock.Transactions))
			for _, tx := range fetchedBlock.Transactions {
				transactions[tx.TxHash()] = tx.TxOut
			}

			blockTxOuts[fetchedBlock.BlockHash()] = transactions
		}

		if txOuts, ok := blockTxOuts[*txDetail.BlockHash][txDetail.Hash]; !ok {
			t.Fatalf("tx (%v) not found in block (%v)",
				txDetail.Hash, txDetail.BlockHash)
		} else {
			var destinationAddresses []stdaddr.Address

			for _, txOut := range txOuts {
				_, addrs :=
					stdscript.ExtractAddrs(txOut.Version, txOut.PkScript,
						&alice.Cfg.NetParams)
				destinationAddresses = append(destinationAddresses, addrs...)
			}

			if !reflect.DeepEqual(txDetail.DestAddresses, destinationAddresses) {
				t.Fatalf("destination addresses mismatch, got %v expected %v",
					txDetail.DestAddresses, destinationAddresses)
			}
		}

		delete(txids, txDetail.Hash)
	}
	if len(txids) != 0 {
		t.Fatalf("all transactions not found in details: left=%v, "+
			"returned_set=%v", spew.Sdump(txids),
			spew.Sdump(txDetails))
	}

	// Next create a transaction paying to an output which isn't under the
	// wallet's control.
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_0)
	outputScript, err := b.Script()
	if err != nil {
		t.Fatalf("unable to make output script: %v", err)
	}
	burnOutput := wire.NewTxOut(outputAmt, outputScript)
	burnTX, err := alice.SendOutputs(
		[]*wire.TxOut{burnOutput}, defaultFeeRate, labels.External,
	)
	if err != nil {
		t.Fatalf("unable to create burn tx: %v", err)
	}
	burnTXID := burnTX.TxHash()
	err = waitForMempoolTx(miner, &burnTXID)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}

	// Before we mine the next block, we'll ensure that the above
	// transaction shows up in the set of unconfirmed transactions returned
	// by ListTransactionDetails.
	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}

	// Query our wallet for transactions from the chain tip, including
	// unconfirmed transactions. The transaction above should be included
	// with a confirmation height of 0, indicating that it has not been
	// mined yet.
	txDetails, err = alice.ListTransactionDetails(
		int32(chainTip), dcrwallet.UnconfirmedHeight,
	)
	if err != nil {
		t.Fatalf("unable to fetch tx details: %v", err)
	}
	var mempoolTxFound bool
	for _, txDetail := range txDetails {
		if !bytes.Equal(txDetail.Hash[:], burnTXID[:]) {
			continue
		}

		// Now that we've found the transaction, ensure that it has a
		// negative number of confirmations to indicate that it's
		// unconfirmed.
		mempoolTxFound = true
		if txDetail.NumConfirmations != 0 {
			t.Fatalf("num confs incorrect, got %v expected %v",
				txDetail.NumConfirmations, 0)
		}
	}
	if !mempoolTxFound {
		t.Fatalf("unable to find mempool tx in tx details!")
	}

	// Generate one block for our transaction to confirm in.
	var numBlocks int32 = 1
	burnBlock, err := vw.GenerateBlocks(context.TODO(), uint32(numBlocks))
	if err != nil {
		t.Fatalf("unable to mine block: %v", err)
	}

	// Progress our chain tip by the number of blocks we have just mined.
	chainTip += int64(numBlocks)

	// Fetch the transaction details again, the new transaction should be
	// shown as debiting from the wallet's balance. Start and end height
	// are inclusive, so we use chainTip for both parameters to get only
	// transactions from the last block.
	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}
	txDetails, err = alice.ListTransactionDetails(
		int32(chainTip), int32(chainTip),
	)
	if err != nil {
		t.Fatalf("unable to fetch tx details: %v", err)
	}
	var burnTxFound bool
	for _, txDetail := range txDetails {
		if !bytes.Equal(txDetail.Hash[:], burnTXID[:]) {
			continue
		}

		burnTxFound = true
		if txDetail.NumConfirmations != 1 {
			t.Fatalf("num confs incorrect, got %v expected %v",
				txDetail.NumConfirmations, 1)
		}

		// We assert that the value is greater than the amount we
		// attempted to send, as the wallet should have paid some amount
		// of network fees.
		if txDetail.Value >= -outputAmt {
			fmt.Println(spew.Sdump(txDetail))
			t.Fatalf("tx value incorrect, got %v expected %v",
				int64(txDetail.Value), -int64(outputAmt))
		}
		if !bytes.Equal(txDetail.BlockHash[:], burnBlock[0][:]) {
			t.Fatalf("block hash mismatch, got %v expected %v",
				txDetail.BlockHash, burnBlock[0])
		}
	}
	if !burnTxFound {
		t.Fatal("tx burning dcr not found")
	}

	// Generate a block which has no wallet transactions in it.
	chainTip += int64(numBlocks)
	_, err = miner.Node.Generate(context.TODO(), uint32(numBlocks))
	if err != nil {
		t.Fatalf("unable to mine block: %v", err)
	}

	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}

	// Query for transactions only in the latest block. We do not expect
	// any transactions to be returned.
	txDetails, err = alice.ListTransactionDetails(
		int32(chainTip), int32(chainTip),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(txDetails) != 0 {
		t.Fatalf("expected 0 transactions, got: %v", len(txDetails))
	}
}

func testTransactionSubscriptions(miner *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// Generate a block and wait for wallet sync to ensure any outstanding
	// txs from previous tests won't interfere with this test.
	if _, err := vw.GenerateBlocks(context.TODO(), 1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}
	err := waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}

	// First, check to see if this wallet meets the TransactionNotifier
	// interface, if not then we'll skip this test for this particular
	// implementation of the WalletController.
	txClient, err := alice.SubscribeTransactions()
	if err != nil {
		t.Skipf("unable to generate tx subscription: %v", err)
	}
	defer txClient.Cancel()

	const (
		outputAmt = dcrutil.AtomsPerCoin
		numTxns   = 3
	)
	errCh1 := make(chan error, 1)
	switch alice.BackEnd() {
	case "neutrino":
		// Neutrino doesn't listen for unconfirmed transactions.
	default:
		go func() {
			for i := 0; i < numTxns; i++ {
				txDetail := <-txClient.UnconfirmedTransactions()
				if txDetail.NumConfirmations != 0 {
					errCh1 <- fmt.Errorf("incorrect number of confs, "+
						"expected %v got %v", 0,
						txDetail.NumConfirmations)
					return
				}
				if txDetail.Value != outputAmt {
					errCh1 <- fmt.Errorf("incorrect output amt, "+
						"expected %v got %v", outputAmt,
						txDetail.Value)
					return
				}
				if txDetail.BlockHash != nil {
					errCh1 <- fmt.Errorf("block hash should be nil, "+
						"is instead %v",
						txDetail.BlockHash)
					return
				}
			}
			errCh1 <- nil
		}()
	}

	// Next, fetch a fresh address from the wallet, create 3 new outputs
	// with the pkScript.
	addrs := make([]stdaddr.Address, numTxns)
	for i := 0; i < numTxns; i++ {
		addrs[i], err = alice.NewAddress(lnwallet.PubKeyHash, false)
		if err != nil {
			t.Fatalf("unable to create new address: %v", err)
		}
	}

	// Sleep to allow the wallet's address manager to close (see issue
	// dcrwallet#1372).
	time.Sleep(time.Millisecond * 50)

	for i := 0; i < numTxns; i++ {
		script, err := input.PayToAddrScript(addrs[i])
		if err != nil {
			t.Fatalf("unable to create output script: %v", err)
		}

		output := &wire.TxOut{
			Value:    outputAmt,
			PkScript: script,
		}
		txid, err := miner.SendOutputs([]*wire.TxOut{output},
			dcrutil.Amount(defaultFeeRate))
		if err != nil {
			t.Fatalf("unable to send coinbase: %v", err)
		}
		err = waitForMempoolTx(miner, txid)
		if err != nil {
			t.Fatalf("tx not relayed to miner: %v", err)
		}
	}

	switch alice.BackEnd() {
	case "neutrino":
		// Neutrino doesn't listen for on unconfirmed transactions.
	default:
		// We should receive a notification for all three transactions
		// generated above.
		select {
		case <-time.After(time.Second * 10):
			t.Fatalf("transactions not received after 10 seconds")
		case err := <-errCh1:
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	errCh2 := make(chan error, 1)
	go func() {
		for i := 0; i < numTxns; i++ {
			txDetail := <-txClient.ConfirmedTransactions()
			if txDetail.NumConfirmations != 1 {
				errCh2 <- fmt.Errorf("incorrect number of confs for %s, expected %v got %v",
					txDetail.Hash, 1, txDetail.NumConfirmations)
				return
			}
			if txDetail.Value != outputAmt {
				errCh2 <- fmt.Errorf("incorrect output amt, expected %v got %v in txid %s",
					outputAmt, txDetail.Value, txDetail.Hash)
				return
			}
		}
		errCh2 <- nil
	}()

	// Next mine a single block, all the transactions generated above
	// should be included.
	if _, err := vw.GenerateBlocks(context.TODO(), 1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// We should receive a notification for all three transactions
	// since they should be mined in the next block.
	select {
	case <-time.After(time.Second * 5):
		t.Fatalf("transactions not received after 5 seconds")
	case err := <-errCh2:
		if err != nil {
			t.Fatal(err)
		}
	}

	// We'll also ensure that the client is able to send our new
	// notifications when we _create_ transactions ourselves that spend our
	// own outputs.
	b := txscript.NewScriptBuilder()
	b.AddOp(txscript.OP_RETURN)
	outputScript, err := b.Script()
	if err != nil {
		t.Fatalf("unable to make output script: %v", err)
	}
	burnOutput := wire.NewTxOut(outputAmt, outputScript)
	tx, err := alice.SendOutputs(
		[]*wire.TxOut{burnOutput}, defaultFeeRate, labels.External,
	)
	if err != nil {
		t.Fatalf("unable to create burn tx: %v", err)
	}
	txid := tx.TxHash()
	err = waitForMempoolTx(miner, &txid)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}

	// Before we mine the next block, we'll ensure that the above
	// transaction shows up in the set of unconfirmed transactions returned
	// by ListTransactionDetails.
	err = waitForWalletSync(miner, alice)
	if err != nil {
		t.Fatalf("Couldn't sync Alice's wallet: %v", err)
	}

	// As we just sent the transaction and it was landed in the mempool, we
	// should get a notification for a new unconfirmed transactions
	select {
	case <-time.After(time.Second * 10):
		t.Fatalf("transactions not received after 10 seconds")
	case unConfTx := <-txClient.UnconfirmedTransactions():
		if unConfTx.Hash != txid {
			t.Fatalf("wrong txn notified: expected %v got %v",
				txid, unConfTx.Hash)
		}
	}
}

// scriptFromKey creates a P2WKH script from the given pubkey.
func scriptFromKey(pubkey *secp256k1.PublicKey) ([]byte, error) {
	pubkeyHash := dcrutil.Hash160(pubkey.SerializeCompressed())
	keyAddr, err := stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(
		pubkeyHash, chaincfg.SimNetParams(),
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create addr: %v", err)
	}
	keyScript, err := input.PayToAddrScript(keyAddr)
	if err != nil {
		return nil, fmt.Errorf("unable to generate script: %v", err)
	}

	return keyScript, nil
}

// mineAndAssert mines a block and ensures the passed TX is part of that block.
func mineAndAssert(r *rpctest.Harness, vw *rpctest.VotingWallet, tx *wire.MsgTx) error {
	txid := tx.TxHash()
	err := waitForMempoolTx(r, &txid)
	if err != nil {
		return fmt.Errorf("tx not relayed to miner: %v", err)
	}

	blockHashes, err := vw.GenerateBlocks(context.TODO(), 1)
	if err != nil {
		return fmt.Errorf("unable to generate block: %v", err)
	}

	block, err := r.Node.GetBlock(context.TODO(), blockHashes[0])
	if err != nil {
		return fmt.Errorf("unable to find block: %v", err)
	}

	if len(block.Transactions) != 2 {
		return fmt.Errorf("expected 2 txs in block, got %d",
			len(block.Transactions))
	}

	blockTx := block.Transactions[1]
	if blockTx.TxHash() != tx.TxHash() {
		return fmt.Errorf("incorrect transaction was mined")
	}

	// Sleep for a second before returning, to make sure the block has
	// propagated.
	time.Sleep(1 * time.Second)
	return nil
}

// txFromOutput takes a tx paying to fromPubKey, and creates a new tx that
// spends the output from this tx, to an address derived from payToPubKey.
func txFromOutput(tx *wire.MsgTx, signer input.Signer, fromPubKey,
	payToPubKey *secp256k1.PublicKey, txFee dcrutil.Amount,
	rbf bool) (*wire.MsgTx, error) {

	// Generate the script we want to spend from.
	keyScript, err := scriptFromKey(fromPubKey)
	if err != nil {
		return nil, fmt.Errorf("unable to generate script: %v", err)
	}

	// We assume the output was paid to the keyScript made earlier.
	var outputIndex uint32
	if len(tx.TxOut) == 1 || bytes.Equal(tx.TxOut[0].PkScript, keyScript) {
		outputIndex = 0
	} else {
		outputIndex = 1
	}
	outputValue := tx.TxOut[outputIndex].Value

	// With the index located, we can create a transaction spending the
	// referenced output.
	tx1 := wire.NewMsgTx()
	tx1.Version = 2

	// If we want to create a tx that signals replacement, set its
	// sequence number to the max one that signals replacement.
	// Otherwise we just use the standard max sequence.
	sequence := wire.MaxTxInSequenceNum
	if rbf {
		sequence = 0xfffffffd // mempool.MaxRBFSequence
	}

	tx1.AddTxIn(&wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  tx.TxHash(),
			Index: outputIndex,
			Tree:  wire.TxTreeRegular,
		},
		Sequence: sequence,
	})

	// Create a script to pay to.
	payToScript, err := scriptFromKey(payToPubKey)
	if err != nil {
		return nil, fmt.Errorf("unable to generate script: %v", err)
	}
	tx1.AddTxOut(&wire.TxOut{
		Value:    outputValue - int64(txFee),
		PkScript: payToScript,
	})

	// Now we can populate the sign descriptor which we'll use to generate
	// the signature.
	signDesc := &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			PubKey: fromPubKey,
		},
		WitnessScript: keyScript,
		Output:        tx.TxOut[outputIndex],
		HashType:      txscript.SigHashAll,
		InputIndex:    0, // Has only one input.
	}

	// With the descriptor created, we use it to generate a signature, then
	// manually create a valid witness stack we'll use for signing.
	spendSig, err := signer.SignOutputRaw(tx1, signDesc)
	if err != nil {
		return nil, fmt.Errorf("unable to generate signature: %v", err)
	}
	witness := make([][]byte, 2)
	witness[0] = append(spendSig.Serialize(), byte(txscript.SigHashAll))
	witness[1] = fromPubKey.SerializeCompressed()
	tx1.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(witness)
	if err != nil {
		return nil, fmt.Errorf("unable to convert witness stack to sigScript: %v", err)
	}

	// Finally, attempt to validate the completed transaction. This should
	// succeed if the wallet was able to properly generate the proper
	// private key.
	vm, err := txscript.NewEngine(
		keyScript, tx1, 0, input.ScriptVerifyFlags, tx.TxOut[outputIndex].Version, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		return nil, fmt.Errorf("spend is invalid: %v", err)
	}

	return tx1, nil
}

// newTx sends coins from Alice's wallet, mines this transaction, and creates a
// new, unconfirmed tx that spends this output to pubKey.
func newTx(t *testing.T, r *rpctest.Harness, vw *rpctest.VotingWallet, pubKey *secp256k1.PublicKey,
	alice *lnwallet.LightningWallet, rbf bool) *wire.MsgTx {
	t.Helper()

	keyScript, err := scriptFromKey(pubKey)
	if err != nil {
		t.Fatalf("unable to generate script: %v", err)
	}

	// Instruct the wallet to fund the output with a newly created
	// transaction.
	newOutput := &wire.TxOut{
		Value:    dcrutil.AtomsPerCoin,
		PkScript: keyScript,
	}
	tx, err := alice.SendOutputs(
		[]*wire.TxOut{newOutput}, defaultFeeRate, labels.External,
	)
	if err != nil {
		t.Fatalf("unable to create output: %v", err)
	}

	// Query for the transaction generated above so we can located the
	// index of our output.
	if err := mineAndAssert(r, vw, tx); err != nil {
		t.Fatalf("unable to mine tx: %v", err)
	}

	// Create a new unconfirmed tx that spends this output.
	txFee := dcrutil.Amount(defaultFeeRate)
	tx1, err := txFromOutput(
		tx, alice.Cfg.Signer, pubKey, pubKey, txFee, rbf,
	)
	if err != nil {
		t.Fatal(err)
	}

	return tx1
}

// testPublishTransaction checks that PublishTransaction returns the expected
// error types in case the transaction being published conflicts with the
// current mempool or chain.
func testPublishTransaction(r *rpctest.Harness, vw *rpctest.VotingWallet,
	alice, _ *lnwallet.LightningWallet, t *testing.T) {

	// Generate a pubkey, and pay-to-addr script.
	keyDesc, err := alice.DeriveNextKey(keychain.KeyFamilyMultiSig)
	if err != nil {
		t.Fatalf("unable to obtain public key: %v", err)
	}

	// We will first check that publishing a transaction already in the
	// mempool does NOT return an error. Create the tx.
	tx1 := newTx(t, r, vw, keyDesc.PubKey, alice, false)

	// Publish the transaction.
	err = alice.PublishTransaction(tx1, labels.External)
	if err != nil {
		t.Fatalf("unable to publish: %v", err)
	}

	txid1 := tx1.TxHash()
	err = waitForMempoolTx(r, &txid1)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}

	// Publish the exact same transaction again. This should not return an
	// error, even though the transaction is already in the mempool.
	err = alice.PublishTransaction(tx1, labels.External)
	if err != nil {
		t.Fatalf("unable to publish: %v", err)
	}

	// Mine the transaction.
	if _, err := vw.GenerateBlocks(context.TODO(), 1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// We'll now test that we don't get an error if we try to publish a
	// transaction that is already mined.
	//
	// Create a new transaction. We must do this to properly test the
	// reject messages from our peers. They might only send us a reject
	// message for a given tx once, so we create a new to make sure it is
	// not just immediately rejected.
	tx2 := newTx(t, r, vw, keyDesc.PubKey, alice, false)

	// Publish this tx.
	err = alice.PublishTransaction(tx2, labels.External)
	if err != nil {
		t.Fatalf("unable to publish: %v", err)
	}

	// Mine the transaction.
	if err := mineAndAssert(r, vw, tx2); err != nil {
		t.Fatalf("unable to mine tx: %v", err)
	}

	// Publish the transaction again. It is already mined, and we don't
	// expect this to return an error.
	err = alice.PublishTransaction(tx2, labels.External)
	if err != nil {
		t.Fatalf("unable to publish: %v", err)
	}

	// We'll do the next mempool check on both RBF and non-RBF enabled
	// transactions.
	var (
		txFee         = dcrutil.Amount(defaultFeeRate * 5)
		tx3, tx3Spend *wire.MsgTx
	)

	// Note: decred does not support rbf at this time, so test only with
	// rbf == false.
	rbfTests := []bool{false}
	for _, rbf := range rbfTests {
		// Now we'll try to double spend an output with a different
		// transaction. Create a new tx and publish it. This is the
		// output we'll try to double spend.
		tx3 = newTx(t, r, vw, keyDesc.PubKey, alice, false)
		err := alice.PublishTransaction(tx3, labels.External)
		if err != nil {
			t.Fatalf("unable to publish: %v", err)
		}

		// Mine the transaction.
		if err := mineAndAssert(r, vw, tx3); err != nil {
			t.Fatalf("unable to mine tx: %v", err)
		}

		// Now we create a transaction that spends the output from the
		// tx just mined.
		tx4, err := txFromOutput(
			tx3, alice.Cfg.Signer, keyDesc.PubKey,
			keyDesc.PubKey, txFee, rbf,
		)
		if err != nil {
			t.Fatal(err)
		}

		// This should be accepted into the mempool.
		err = alice.PublishTransaction(tx4, labels.External)
		if err != nil {
			t.Fatalf("unable to publish: %v", err)
		}

		// Keep track of the last successfully published tx to spend
		// tx3.
		tx3Spend = tx4

		txid4 := tx4.TxHash()
		err = waitForMempoolTx(r, &txid4)
		if err != nil {
			t.Fatalf("tx not relayed to miner: %v", err)
		}

		// Create a new key we'll pay to, to ensure we create a unique
		// transaction.
		keyDesc2, err := alice.DeriveNextKey(
			keychain.KeyFamilyMultiSig,
		)
		if err != nil {
			t.Fatalf("unable to obtain public key: %v", err)
		}

		// Create a new transaction that spends the output from tx3,
		// and that pays to a different address. We expect this to be
		// rejected because it is a double spend.
		tx5, err := txFromOutput(
			tx3, alice.Cfg.Signer, keyDesc.PubKey,
			keyDesc2.PubKey, txFee, rbf,
		)
		if err != nil {
			t.Fatal(err)
		}

		err = alice.PublishTransaction(tx5, labels.External)
		if err != lnwallet.ErrDoubleSpend {
			t.Fatalf("expected ErrDoubleSpend, got: %v", err)
		}

		// Create another transaction that spends the same output, but
		// has a higher fee. We expect also this tx to be rejected for
		// non-RBF enabled transactions, while it should succeed
		// otherwise.
		pubKey3, err := alice.DeriveNextKey(keychain.KeyFamilyMultiSig)
		if err != nil {
			t.Fatalf("unable to obtain public key: %v", err)
		}
		tx6, err := txFromOutput(
			tx3, alice.Cfg.Signer, keyDesc.PubKey,
			pubKey3.PubKey, 2*txFee, rbf,
		)
		if err != nil {
			t.Fatal(err)
		}

		// Expect rejection in non-RBF case.
		expErr := lnwallet.ErrDoubleSpend
		if rbf {
			// Expect success in rbf case.
			expErr = nil
			tx3Spend = tx6
		}
		err = alice.PublishTransaction(tx6, labels.External)
		if err != expErr {
			t.Fatalf("expected ErrDoubleSpend, got: %v", err)
		}

		// Mine the tx spending tx3.
		if err := mineAndAssert(r, vw, tx3Spend); err != nil {
			t.Fatalf("unable to mine tx: %v", err)
		}
	}

	// At last we try to spend an output already spent by a confirmed
	// transaction.
	//
	// TODO(halseth): we currently skip this test for neutrino, as the
	// backing btcd node will consider the tx being an orphan, and will
	// accept it. Should look into if this is the behavior also for
	// bitcoind, and update test accordingly.
	//
	// TODO(decred) wallet/v3 also changed the semantics to simply ignore
	// spent output errors and return nil when publishing a double spend
	// that was already mined, so we ignore this test as well.
	if alice.BackEnd() == "disabled" {
		// Create another tx spending tx3.
		pubKey4, err := alice.DeriveNextKey(
			keychain.KeyFamilyMultiSig,
		)
		if err != nil {
			t.Fatalf("unable to obtain public key: %v", err)
		}
		tx7, err := txFromOutput(
			tx3, alice.Cfg.Signer, keyDesc.PubKey,
			pubKey4.PubKey, txFee, false,
		)

		if err != nil {
			t.Fatal(err)
		}

		// Expect rejection.
		err = alice.PublishTransaction(tx7, labels.External)
		if err != lnwallet.ErrDoubleSpend {
			t.Fatalf("expected ErrDoubleSpend, got: %v", err)
		}
	}
}

func testSignOutputUsingTweaks(r *rpctest.Harness,
	avw *rpctest.VotingWallet, alice, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// We'd like to test the ability of the wallet's input.Signer implementation
	// to be able to sign with a private key derived from tweaking the
	// specific public key. This scenario exercises the case when the
	// wallet needs to sign for a sweep of a revoked output, or just claim
	// any output that pays to a tweaked key.

	// First, generate a new public key under the control of the wallet,
	// then generate a revocation key using it.
	pubKey, err := alice.DeriveNextKey(
		keychain.KeyFamilyMultiSig,
	)
	if err != nil {
		t.Fatalf("unable to obtain public key: %v", err)
	}

	// As we'd like to test both single tweak, and double tweak spends,
	// we'll generate a commitment pre-image, then derive a revocation key
	// and single tweak from that.
	commitPreimage := bytes.Repeat([]byte{2}, 32)
	commitSecret := secp256k1.PrivKeyFromBytes(commitPreimage)
	commitPoint := commitSecret.PubKey()

	revocationKey := input.DeriveRevocationPubkey(pubKey.PubKey, commitPoint)
	commitTweak := input.SingleTweakBytes(commitPoint, pubKey.PubKey)

	tweakedPub := input.TweakPubKey(pubKey.PubKey, commitPoint)

	// As we'd like to test both single and double tweaks, we'll repeat
	// the same set up twice. The first will use a regular single tweak,
	// and the second will use a double tweak.
	baseKey := pubKey
	for i := 0; i < 2; i++ {
		var tweakedKey *secp256k1.PublicKey
		if i == 0 {
			tweakedKey = tweakedPub
		} else {
			tweakedKey = revocationKey
		}

		// Using the given key for the current iteration, we'll
		// generate a regular p2wkh from that.
		pubkeyHash := dcrutil.Hash160(tweakedKey.SerializeCompressed())
		keyAddr, err := stdaddr.NewAddressPubKeyHashEcdsaSecp256k1V0(pubkeyHash,
			netParams)
		if err != nil {
			t.Fatalf("unable to create addr: %v", err)
		}
		keyScript, err := input.PayToAddrScript(keyAddr)
		if err != nil {
			t.Fatalf("unable to generate script: %v", err)
		}

		// With the script fully assembled, instruct the wallet to fund
		// the output with a newly created transaction.
		newOutput := &wire.TxOut{
			Value:    dcrutil.AtomsPerCoin,
			PkScript: keyScript,
			Version:  scriptVersion,
		}
		tx, err := alice.SendOutputs(
			[]*wire.TxOut{newOutput}, defaultFeeRate, labels.External,
		)
		if err != nil {
			t.Fatalf("unable to create output: %v", err)
		}
		txid := tx.TxHash()
		// Query for the transaction generated above so we can located
		// the index of our output.
		err = waitForMempoolTx(r, &txid)
		if err != nil {
			t.Fatalf("tx not relayed to miner: %v", err)
		}
		var outputIndex uint32
		if bytes.Equal(tx.TxOut[0].PkScript, keyScript) {
			outputIndex = 0
		} else {
			outputIndex = 1
		}

		// With the index located, we can create a transaction spending
		// the referenced output.
		sweepTx := wire.NewMsgTx()
		sweepTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{
				Hash:  txid,
				Index: outputIndex,
				Tree:  wire.TxTreeRegular,
			},
		})
		sweepTx.AddTxOut(&wire.TxOut{
			Value:    1000,
			PkScript: keyScript,
			Version:  scriptVersion,
		})

		// Now we can populate the sign descriptor which we'll use to
		// generate the signature. Within the descriptor we set the
		// private tweak value as the key in the script is derived
		// based on this tweak value and the key we originally
		// generated above.
		signDesc := &input.SignDescriptor{
			KeyDesc: keychain.KeyDescriptor{
				PubKey: baseKey.PubKey,
			},
			WitnessScript: keyScript,
			Output:        newOutput,
			HashType:      txscript.SigHashAll,
			InputIndex:    0,
		}

		// If this is the first, loop, we'll use the generated single
		// tweak, otherwise, we'll use the double tweak.
		if i == 0 {
			signDesc.SingleTweak = commitTweak
		} else {
			signDesc.DoubleTweak = commitSecret
		}

		// With the descriptor created, we use it to generate a
		// signature, then manually create a valid witness stack we'll
		// use for signing.
		spendSig, err := alice.Cfg.Signer.SignOutputRaw(sweepTx, signDesc)
		if err != nil {
			t.Fatalf("unable to generate signature: %v", err)
		}
		witness := make([][]byte, 2)
		witness[0] = append(spendSig.Serialize(), byte(txscript.SigHashAll))
		witness[1] = tweakedKey.SerializeCompressed()
		sweepTx.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(witness)
		if err != nil {
			t.Fatalf("unable to convert witness stack to sigScript: %v", err)
		}

		// Finally, attempt to validate the completed transaction. This
		// should succeed if the wallet was able to properly generate
		// the proper private key.
		vm, err := txscript.NewEngine(keyScript,
			sweepTx, 0, input.ScriptVerifyFlags, newOutput.Version, nil)
		if err != nil {
			t.Fatalf("unable to create engine: %v", err)
		}
		if err := vm.Execute(); err != nil {
			t.Fatalf("spend #%v is invalid: %v", i, err)
		}
	}
}

func testReorgWalletBalance(r *rpctest.Harness, vw *rpctest.VotingWallet,
	w *lnwallet.LightningWallet, _ *lnwallet.LightningWallet,
	t *testing.T) {

	// Currently disabled due to
	// https://github.com/decred/dcrwallet/issues/1710. Re-assess after
	// that is fixed.
	if w.BackEnd() == "dcrw-spv" {
		t.Skipf("Skipping for SPV for the moment")
	}

	// We first mine a few blocks to ensure any transactions still in the
	// mempool confirm, and then get the original balance, before a
	// reorganization that doesn't invalidate any existing transactions or
	// create any new non-coinbase transactions. We'll then check if it's
	// the same after the empty reorg.
	_, err := vw.GenerateBlocks(context.TODO(), 5)
	if err != nil {
		t.Fatalf("unable to generate blocks on passed node: %v", err)
	}

	// Give wallet time to catch up.
	err = waitForWalletSync(r, w)
	if err != nil {
		t.Fatalf("unable to sync wallet: %v", err)
	}

	// Send some money from the miner to the wallet
	err = loadTestCredits(r, w, vw.GenerateBlocks, 20, 4)
	if err != nil {
		t.Fatalf("unable to send money to lnwallet: %v", err)
	}

	// Send some money from the wallet back to the miner.
	// Grab a fresh address from the miner to house this output.
	minerAddr, err := r.NewAddress()
	if err != nil {
		t.Fatalf("unable to generate address for miner: %v", err)
	}
	script, err := input.PayToAddrScript(minerAddr)
	if err != nil {
		t.Fatalf("unable to create pay to addr script: %v", err)
	}
	output := &wire.TxOut{
		Value:    1e8,
		PkScript: script,
	}
	tx, err := w.SendOutputs(
		[]*wire.TxOut{output}, defaultFeeRate, labels.External,
	)
	if err != nil {
		t.Fatalf("unable to send outputs: %v", err)
	}
	txid := tx.TxHash()
	err = waitForMempoolTx(r, &txid)
	if err != nil {
		t.Fatalf("tx not relayed to miner: %v", err)
	}
	_, err = vw.GenerateBlocks(context.TODO(), 3)
	if err != nil {
		t.Fatalf("unable to generate blocks on passed node: %v", err)
	}

	// Give wallet time to catch up.
	err = waitForWalletSync(r, w)
	if err != nil {
		t.Fatalf("unable to sync wallet: %v", err)
	}

	// Get the original balance.
	origBalance, err := w.ConfirmedBalance(1)
	if err != nil {
		t.Fatalf("unable to query for balance: %v", err)
	}

	// Now we cause a reorganization as follows.
	// Step 1: create a new miner and start it.
	r2, err := testutils.NewSetupRPCTest(t, 5, r.ActiveNet, nil, []string{"--txindex"}, false, 0)
	if err != nil {
		t.Fatalf("unable to create temp miner: %v", err)
	}
	defer r2.TearDown()
	newBalance, err := w.ConfirmedBalance(1)
	if err != nil {
		t.Fatalf("unable to query for balance: %v", err)
	}
	if origBalance != newBalance {
		t.Fatalf("wallet balance incorrect, should have %v, "+
			"instead have %v", origBalance, newBalance)
	}

	// Step 2: connect the miner to the passed miner and wait for
	// synchronization.
	err = rpctest.ConnectNode(r2, r)
	if err != nil {
		t.Fatalf("unable to connect mining nodes together: %v", err)
	}
	err = rpctest.JoinNodes([]*rpctest.Harness{r2, r}, rpctest.Blocks)
	if err != nil {
		t.Fatalf("unable to synchronize mining nodes: %v", err)
	}

	mineFirst := func(nb uint32) ([]*chainhash.Hash, error) {
		return vw.GenerateBlocks(context.TODO(), nb)
	}
	mineSecond := func(nb uint32) ([]*chainhash.Hash, error) {
		return testutils.AdjustedSimnetMiner(r2.Node, nb)
	}

	// Step 3: Do a set of reorgs by disconnecting the two miners, mining
	// one block on the passed miner and two on the created miner,
	// connecting them, and waiting for them to sync.
	for i := 0; i < 5; i++ {
		// Wait for disconnection
		timeout := time.After(30 * time.Second)
		stillConnected := true
		for stillConnected {
			// Allow for timeout
			time.Sleep(100 * time.Millisecond)
			select {
			case <-timeout:
				t.Fatalf("timeout waiting for miner disconnect")
			default:
			}
			err = rpctest.RemoveNode(context.TODO(), r2, r)
			if err != nil {
				t.Fatalf("unable to disconnect mining nodes: %v",
					err)
			}
			stillConnected, err = rpctest.NodesConnected(context.TODO(), r2, r, true)
			if err != nil {
				t.Fatalf("error checking node connectivity: %v",
					err)
			}
		}
		_, err = mineFirst(2)
		if err != nil {
			t.Fatalf("unable to generate blocks on passed node: %v",
				err)
		}
		_, err = mineSecond(3)
		if err != nil {
			t.Fatalf("unable to generate blocks on created node: %v",
				err)
		}

		// Give wallet time to catch up so we force a full reorg.
		err = waitForWalletSync(r, w)
		if err != nil {
			t.Fatalf("unable to sync wallet: %v", err)
		}

		// Step 5: Reconnect the miners and wait for them to synchronize.
		err = rpctest.ConnectNode(r2, r)
		if err != nil {
			switch err := err.(type) {
			case *dcrjson.RPCError:
				if err.Code != -8 {
					t.Fatalf("unable to connect mining "+
						"nodes together: %v", err)
				}
			default:
				t.Fatalf("unable to connect mining nodes "+
					"together: %v", err)
			}
		}
		err = rpctest.JoinNodes([]*rpctest.Harness{r2, r},
			rpctest.Blocks)
		if err != nil {
			t.Fatalf("unable to synchronize mining nodes: %v", err)
		}

		// Give wallet time to catch up.
		err = waitForWalletSync(r, w)
		if err != nil {
			t.Fatalf("unable to sync wallet: %v", err)
		}
	}

	// Now we check that the wallet balance stays the same.
	newBalance, err = w.ConfirmedBalance(1)
	if err != nil {
		t.Fatalf("unable to query for balance: %v", err)
	}
	if origBalance != newBalance {
		t.Fatalf("wallet balance incorrect, should have %v, "+
			"instead have %v", origBalance, newBalance)
	}
}

// testChangeOutputSpendConfirmation ensures that when we attempt to spend a
// change output created by the wallet, the wallet receives its confirmation
// once included in the chain.
func testChangeOutputSpendConfirmation(r *rpctest.Harness,
	vw *rpctest.VotingWallet, alice, bob *lnwallet.LightningWallet,
	t *testing.T) {

	// In order to test that we see the confirmation of a transaction that
	// spends an output created by SendOutputs, we'll start by emptying
	// Alice's wallet so that no other UTXOs can be picked. To do so, we'll
	// generate an address for Bob, who will receive all the coins.
	// Assuming a balance of 80 DCR and a transaction fee of 2500 atom/kB,
	// we'll craft the following transaction so that Alice doesn't have any
	// UTXOs left.
	aliceBalance, err := alice.ConfirmedBalance(0)
	if err != nil {
		t.Fatalf("unable to retrieve alice's balance: %v", err)
	}
	bobPkScript := newPkScript(t, bob, lnwallet.PubKeyHash)

	// A transaction to sweep all 80 DCR that should be in the wallet will
	// be estimated to have ~3407 bytes, so calculate what a tx of that size
	// will pay in fees and remove from the total amount.
	//
	// TODO(wilmer): replace this once SendOutputs easily supports sending
	// all funds in one transaction.
	txFeeRate := defaultFeeRate
	txFee := txFeeRate.FeeForSize(3407)
	output := &wire.TxOut{
		Value:    int64(aliceBalance - txFee),
		PkScript: bobPkScript,
	}
	tx := sendCoins(t, r, vw, alice, bob, output, txFeeRate)
	txHash := tx.TxHash()
	assertTxInWallet(t, alice, txHash, true)
	assertTxInWallet(t, bob, txHash, true)

	// With the transaction sent and confirmed, Alice's balance should now
	// be 0.
	aliceBalance, err = alice.ConfirmedBalance(0)
	if err != nil {
		t.Fatalf("unable to retrieve alice's balance: %v", err)
	}
	if aliceBalance != 0 {
		t.Fatalf("expected alice's balance to be 0 DCR, found %v",
			aliceBalance)
	}

	// Now, we'll send an output back to Alice from Bob of 1 DCR.
	alicePkScript := newPkScript(t, alice, lnwallet.PubKeyHash)
	output = &wire.TxOut{
		Value:    dcrutil.AtomsPerCoin,
		PkScript: alicePkScript,
		Version:  scriptVersion,
	}
	tx = sendCoins(t, r, vw, bob, alice, output, txFeeRate)
	txHash = tx.TxHash()
	assertTxInWallet(t, alice, txHash, true)
	assertTxInWallet(t, bob, txHash, true)

	// Alice now has an available output to spend, but it was not a change
	// output, which is what the test expects. Therefore, we'll generate one
	// by sending Bob back some coins.
	output = &wire.TxOut{
		Value:    dcrutil.AtomsPerCent,
		PkScript: bobPkScript,
	}
	tx = sendCoins(t, r, vw, alice, bob, output, txFeeRate)
	txHash = tx.TxHash()
	assertTxInWallet(t, alice, txHash, true)
	assertTxInWallet(t, bob, txHash, true)

	// Then, we'll spend the change output and ensure we see its
	// confirmation come in.
	tx = sendCoins(t, r, vw, alice, bob, output, txFeeRate)
	txHash = tx.TxHash()
	assertTxInWallet(t, alice, txHash, true)
	assertTxInWallet(t, bob, txHash, true)

	// Finally, we'll replenish Alice's wallet with some more coins to
	// ensure she has enough for any following test cases.
	if err := loadTestCredits(r, alice, vw.GenerateBlocks, 20, 4); err != nil {
		t.Fatalf("unable to replenish alice's wallet: %v", err)
	}
}

// testLastUnusedAddr tests that the LastUnusedAddress returns the address if
// it isn't used, and also that once the address becomes used, then it's
// properly rotated.
func testLastUnusedAddr(miner *rpctest.Harness,
	vw *rpctest.VotingWallet,
	alice, bob *lnwallet.LightningWallet, t *testing.T) {

	// This is unimplemented on remotedcrwallet, but it's a bad
	// idea to use anyway and isn't currently used anywhere in the
	// code except the rpcserver, which we don't use anyway.
	if alice.BackEnd() == "remotedcrwallet" {
		t.Skip()
	}

	if _, err := vw.GenerateBlocks(context.TODO(), 1); err != nil {
		t.Fatalf("unable to generate block: %v", err)
	}

	// We'll repeat this test for each address type to ensure they're all
	// rotated properly.
	addrTypes := []lnwallet.AddressType{
		lnwallet.PubKeyHash,
	}
	for _, addrType := range addrTypes {
		addr1, err := alice.LastUnusedAddress(addrType)
		if err != nil {
			t.Fatalf("unable to get addr: %v", err)
		}
		addr2, err := alice.LastUnusedAddress(addrType)
		if err != nil {
			t.Fatalf("unable to get addr: %v", err)
		}

		// If we generate two addresses back to back, then we should
		// get the same addr, as none of them have been used yet.
		if addr1.String() != addr2.String() {
			t.Fatalf("addresses changed w/o use: %v vs %v", addr1, addr2)
		}

		// Next, we'll have Bob pay to Alice's new address. This should
		// trigger address rotation at the backend wallet.
		addrScript, err := input.PayToAddrScript(addr1)
		if err != nil {
			t.Fatalf("unable to convert addr to script: %v", err)
		}
		feeRate := chainfee.AtomPerKByte(1e4)
		output := &wire.TxOut{
			Value:    1000000,
			PkScript: addrScript,
		}
		sendCoins(t, miner, vw, bob, alice, output, feeRate)

		// If we make a new address, then it should be brand new, as
		// the prior address has been used.
		addr3, err := alice.LastUnusedAddress(addrType)
		if err != nil {
			t.Fatalf("unable to get addr: %v", err)
		}
		if addr1.String() == addr3.String() {
			t.Fatalf("address should have changed but didn't")
		}
	}
}

// testCreateSimpleTx checks that a call to CreateSimpleTx will return a
// transaction that is equal to the one that is being created by SendOutputs in
// a subsequent call.
func testCreateSimpleTx(r *rpctest.Harness, // nolint: unused
	vw *rpctest.VotingWallet,
	w, _ *lnwallet.LightningWallet, t *testing.T) {

	if _, is := w.WalletController.(*dcrwallet.DcrWallet); !is {
		return
	}

	// Send some money from the miner to the wallet
	err := loadTestCredits(r, w, vw.GenerateBlocks, 20, 4)
	if err != nil {
		t.Fatalf("unable to send money to lnwallet: %v", err)
	}

	const txOverhead int64 = 12 // version, locktime and expiry
	changeSize := int64(txsizes.EstimateOutputSize(txsizes.P2PKHPkScriptSize))
	inputSize := txsizes.EstimateInputSize(txsizes.RedeemP2PKHSigScriptSize)

	outputSerializeSize := func(scriptLen int64) int64 {
		return 8 + 2 + int64(wire.VarIntSerializeSize(uint64(scriptLen))) + scriptLen
	}

	const p2pkhOutputPkScriptSize = 25
	const p2shOutputPkScriptSize = 23

	calcSize := func(numInputs int) int64 {
		return txOverhead +
			int64(2*wire.VarIntSerializeSize(uint64(numInputs))) +
			int64(wire.VarIntSerializeSize(uint64(2))) +
			int64(inputSize*numInputs) +
			outputSerializeSize(p2pkhOutputPkScriptSize) +
			outputSerializeSize(p2shOutputPkScriptSize) +
			changeSize
	}

	calcFee := func(feeRate chainfee.AtomPerKByte, numInputs int) int64 {
		return int64(feeRate.FeeForSize(calcSize(numInputs)))
	}

	// The test cases we will run through for all backends.
	testCases := []struct {
		outVals []int64
		feeRate chainfee.AtomPerKByte
		expFees int64
		invalid bool
	}{
		{
			outVals: []int64{},
			feeRate: 2500,
			invalid: true,
		},

		{
			outVals: []int64{1e3},
			feeRate: 2500,
			invalid: true, // Dust output.
		},

		{
			outVals: []int64{1e8},
			expFees: calcFee(2500, 1),
			feeRate: 2500,
		},
		// {
		// 	outVals: []int64{1e8, 2e8, 1e8, 2e7, 3e5},
		// 	feeRate: 2500,
		// },
		// {
		// 	outVals: []int64{1e8, 2e8, 1e8, 2e7, 3e5},
		// 	feeRate: 12500,
		// },
		// {
		// 	outVals: []int64{1e8, 2e8, 1e8, 2e7, 3e5},
		// 	feeRate: 50000,
		// },
		// {
		// 	outVals: []int64{1e8, 2e8, 1e8, 2e7, 3e5, 1e8, 2e8,
		// 		1e8, 2e7, 3e5},
		// 	feeRate: 44250,
		// },
	}

	for _, test := range testCases {
		feeRate := test.feeRate

		// Grab some fresh addresses from the miner that we will send
		// to.
		outputs := make([]*wire.TxOut, len(test.outVals))
		for i, outVal := range test.outVals {
			h := [20]byte{0x0a}
			p2shAddr, _ := stdaddr.NewAddressScriptHashV0FromHash(h[:], r.ActiveNet)
			script, err := input.PayToAddrScript(p2shAddr)
			if err != nil {
				t.Fatalf("unable to create pay to addr "+
					"script: %v", err)
			}
			output := &wire.TxOut{
				Value:    outVal,
				PkScript: script,
			}

			outputs[i] = output
		}

		// Now try creating a tx spending to these outputs.
		fee, err := w.EstimateTxFee(outputs, feeRate)
		if err != nil {
			if test.invalid {
				continue
			}
			t.Fatalf("EstimteTxFee error when none expected: %v", err)
		} else if test.invalid {
			t.Fatalf("No EstimteTxFee error when one was expected")
		}

		if test.expFees != fee {
			t.Fatalf("wrong fees. expected %d, got %d", test.expFees, fee)
		}
	}
}

type walletTestCase struct {
	name string
	test func(miner *rpctest.Harness, vw *rpctest.VotingWallet,
		alice, bob *lnwallet.LightningWallet, test *testing.T)
}

var walletTests = []walletTestCase{
	{
		// TODO(wilmer): this test should remain first until the wallet
		// can properly craft a transaction that spends all of its
		// on-chain funds.
		name: "change output spend confirmation",
		test: testChangeOutputSpendConfirmation,
	},
	{
		// This test also needs to happen at the start of testing, to
		// prevent a reorg past SVH which could cause the voting wallet
		// to misbehave.
		name: "reorg wallet balance",
		test: testReorgWalletBalance,
	},
	{
		name: "insane fee reject",
		test: testReservationInitiatorBalanceBelowDustCancel,
	},
	{
		name: "single funding workflow",
		test: func(miner *rpctest.Harness, vw *rpctest.VotingWallet,
			alice, bob *lnwallet.LightningWallet, t *testing.T) {

			testSingleFunderReservationWorkflow(
				miner, vw, alice, bob, t,
				lnwallet.CommitmentTypeLegacy, nil, nil,
				[32]byte{}, 0,
			)
		},
	},
	{
		name: "single funding workflow tweakless",
		test: func(miner *rpctest.Harness, vw *rpctest.VotingWallet,
			alice, bob *lnwallet.LightningWallet, t *testing.T) {

			testSingleFunderReservationWorkflow(
				miner, vw, alice, bob, t,
				lnwallet.CommitmentTypeTweakless, nil, nil,
				[32]byte{}, 0,
			)
		},
	},
	{
		name: "single funding workflow external funding tx",
		test: testSingleFunderExternalFundingTx,
	},
	{
		name: "dual funder workflow",
		test: testDualFundingReservationWorkflow,
	},
	{
		name: "output locking",
		test: testFundingTransactionLockedOutputs,
	},
	{
		name: "reservation insufficient funds",
		test: testFundingCancellationNotEnoughFunds,
	},
	{
		name: "transaction subscriptions",
		test: testTransactionSubscriptions,
	},
	{
		name: "transaction details",
		test: testListTransactionDetails,
	},
	{
		name: "publish transaction",
		test: testPublishTransaction,
	},
	{
		name: "signed with tweaked pubkeys",
		test: testSignOutputUsingTweaks,
	},
	{
		name: "test cancel non-existent reservation",
		test: testCancelNonExistentReservation,
	},
	{
		name: "last unused addr",
		test: testLastUnusedAddr,
	},
	// TODO(decred) re-enable these tests after implementing.
	// { name: "create simple tx", test: testCreateSimpleTx, },
	// { name: "test get recovery info", test: testGetRecoveryInfo, },
}

func clearWalletStates(a, b *lnwallet.LightningWallet) error {
	a.ResetReservations()
	b.ResetReservations()

	if err := a.Cfg.Database.Wipe(); err != nil {
		return err
	}

	return b.Cfg.Database.Wipe()
}

func waitForMempoolTx(r *rpctest.Harness, txid *chainhash.Hash) error {
	var found bool
	var tx *dcrutilv3.Tx
	var err error
	timeout := time.After(30 * time.Second)
	for !found {
		// Do a short wait
		select {
		case <-timeout:
			return fmt.Errorf("timeout after 10s")
		default:
		}
		time.Sleep(100 * time.Millisecond)

		// Check for the harness' knowledge of the txid
		tx, err = r.Node.GetRawTransaction(context.TODO(), txid)
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

func waitForWalletSync(r *rpctest.Harness, w *lnwallet.LightningWallet) error {
	var (
		synced      bool
		err         error
		predErr     error
		bestHash    *chainhash.Hash
		knownHash   chainhash.Hash
		knownHeight int64
		bestHeight  int64
	)
	timeout := time.After(10 * time.Second)
	for !synced {
		// Do a short wait
		select {
		case <-timeout:
			return fmt.Errorf("timeout after 30s. predErr=%v", predErr)
		case <-time.Tick(100 * time.Millisecond):
		}

		// Check whether the chain source of the wallet is caught up to
		// the harness it's supposed to be catching up to.
		bestHash, bestHeight, err = r.Node.GetBestBlock(context.TODO())
		if err != nil {
			return fmt.Errorf("error getting node best block: %v", err)
		}
		knownHeight, knownHash, _, err = w.BestBlock()
		if err != nil {
			return fmt.Errorf("error getting chainIO bestBlock: %v", err)
		}
		if knownHeight != bestHeight {
			predErr = fmt.Errorf("miner and wallet best heights "+
				"are not the same (want=%d got=%d)",
				bestHeight, knownHeight)
			continue
		}
		if knownHash != *bestHash {
			return fmt.Errorf("hash at height %d doesn't match: "+
				"expected %s, got %s", bestHeight, bestHash,
				knownHash)
		}

		// Check for synchronization.
		synced, _, err = w.IsSynced()
		if err != nil {
			return err
		}
		if !synced {
			predErr = fmt.Errorf("wallet isSynced=false")
		}
	}
	return nil
}

// testSingleFunderExternalFundingTx tests that the wallet is able to properly
// carry out a funding flow backed by a channel point that has been crafted
// outside the wallet.
func testSingleFunderExternalFundingTx(miner *rpctest.Harness,
	vw *rpctest.VotingWallet,
	alice, bob *lnwallet.LightningWallet, t *testing.T) {

	// First, we'll obtain multi-sig keys from both Alice and Bob which
	// simulates them exchanging keys on a higher level.
	aliceFundingKey, err := alice.DeriveNextKey(keychain.KeyFamilyMultiSig)
	if err != nil {
		t.Fatalf("unable to obtain alice funding key: %v", err)
	}
	bobFundingKey, err := bob.DeriveNextKey(keychain.KeyFamilyMultiSig)
	if err != nil {
		t.Fatalf("unable to obtain bob funding key: %v", err)
	}

	// We'll now set up for them to open a 4 BTC channel, with 1 BTC pushed
	// to Bob's side.
	chanAmt := 4 * dcrutil.AtomsPerCoin

	// Simulating external funding negotiation, we'll now create the
	// funding transaction for both parties. Utilizing existing tools,
	// we'll create a new chanfunding.Assembler hacked by Alice's wallet.
	aliceChanFunder := chanfunding.NewWalletAssembler(chanfunding.WalletConfig{
		CoinSource:       lnwallet.NewCoinSource(alice),
		CoinSelectLocker: alice,
		CoinLocker:       alice,
		Signer:           alice.Cfg.Signer,
		DustLimit:        600,
	})

	// With the chan funder created, we'll now provision a funding intent,
	// bind the keys we obtained above, and finally obtain our funding
	// transaction and outpoint.
	fundingIntent, err := aliceChanFunder.ProvisionChannel(&chanfunding.Request{
		LocalAmt: dcrutil.Amount(chanAmt),
		MinConfs: 1,
		FeeRate:  1e4,
		ChangeAddr: func() (stdaddr.Address, error) {
			return alice.NewAddress(lnwallet.PubKeyHash, true)
		},
	})
	if err != nil {
		t.Fatalf("unable to perform coin selection: %v", err)
	}

	// With our intent created, we'll instruct it to finalize the funding
	// transaction, and also hand us the outpoint so we can simulate
	// external crafting of the funding transaction.
	var (
		fundingTx *wire.MsgTx
		chanPoint *wire.OutPoint
	)
	if fullIntent, ok := fundingIntent.(*chanfunding.FullIntent); ok {
		fullIntent.BindKeys(&aliceFundingKey, bobFundingKey.PubKey)

		fundingTx, err = fullIntent.CompileFundingTx(nil, nil)
		if err != nil {
			t.Fatalf("unable to compile funding tx: %v", err)
		}
		chanPoint, err = fullIntent.ChanPoint()
		if err != nil {
			t.Fatalf("unable to obtain chan point: %v", err)
		}
	} else {
		t.Fatalf("expected full intent, instead got: %T", fullIntent)
	}

	// Now that we have the fully constructed funding transaction, we'll
	// create a new shim external funder out of it for Alice, and prep a
	// shim intent for Bob.
	thawHeight := uint32(200)
	aliceExternalFunder := chanfunding.NewCannedAssembler(
		thawHeight, *chanPoint, dcrutil.Amount(chanAmt), &aliceFundingKey,
		bobFundingKey.PubKey, true,
	)
	bobShimIntent, err := chanfunding.NewCannedAssembler(
		thawHeight, *chanPoint, dcrutil.Amount(chanAmt), &bobFundingKey,
		aliceFundingKey.PubKey, false,
	).ProvisionChannel(&chanfunding.Request{
		LocalAmt: dcrutil.Amount(chanAmt),
		MinConfs: 1,
		FeeRate:  1e4,
		ChangeAddr: func() (stdaddr.Address, error) {
			return bob.NewAddress(lnwallet.PubKeyHash, true)
		},
	})
	if err != nil {
		t.Fatalf("unable to create shim intent for bob: %v", err)
	}

	// At this point, we have everything we need to carry out our test, so
	// we'll being the funding flow between Alice and Bob.
	//
	// However, before we do so, we'll register a new shim intent for Bob,
	// so he knows what keys to use when he receives the funding request
	// from Alice.
	pendingChanID := testHdSeed
	err = bob.RegisterFundingIntent(pendingChanID, bobShimIntent)
	if err != nil {
		t.Fatalf("unable to register intent: %v", err)
	}

	// Now we can carry out the single funding flow as normal, we'll
	// specify our external funder and funding transaction, as well as the
	// pending channel ID generated above to allow Alice and Bob to track
	// the funding flow externally.
	testSingleFunderReservationWorkflow(
		miner, vw, alice, bob, t, lnwallet.CommitmentTypeTweakless,
		aliceExternalFunder, func() *wire.MsgTx {
			return fundingTx
		}, pendingChanID, thawHeight,
	)
}

// TestInterfaces tests all registered interfaces with a unified set of tests
// which exercise each of the required methods found within the WalletController
// interface.
//
// NOTE: In the future, when additional implementations of the WalletController
// interface have been implemented, in order to ensure the new concrete
// implementation is automatically tested, two steps must be undertaken. First,
// one needs add a "non-captured" (_) import from the new sub-package. This
// import should trigger an init() method within the package which registers
// the interface. Second, an additional case in the switch within the main loop
// below needs to be added which properly initializes the interface.
//
// TODO(roasbeef): purge bobNode in favor of dual lnwallet's
func TestLightningWallet(t *testing.T) {
	var (
		miningNode *rpctest.Harness
	)
	defer func() {
		if miningNode != nil {
			miningNode.TearDown()
		}
	}()

	for _, walletDriver := range lnwallet.RegisteredWallets() {
		for _, backEnd := range walletDriver.BackEnds() {
			// Initialize the harness around a dcrd node which will
			// serve as our dedicated miner to generate blocks,
			// cause re-orgs, etc. We'll set up this node with a
			// chain length of 125, so we have plenty of DCR to
			// play around with.
			minerLogDir := fmt.Sprintf(".miner-logs-%s-%s",
				walletDriver.WalletType, backEnd)
			minerArgs := []string{"--txindex", "--debuglevel=debug",
				"--logdir=" + minerLogDir}
			miningNode, err := testutils.NewSetupRPCTest(
				t, 5, netParams, nil, minerArgs, true, 0,
			)
			if err != nil {
				t.Fatalf("unable to create mining node: %v", err)
			}

			// Generate the premine block.
			_, err = miningNode.Node.Generate(context.TODO(), 1)
			if err != nil {
				t.Fatalf("unable to generate premine: %v", err)
			}

			// Generate enough blocks for the initial load of test and voting
			// wallet but not so many that it would trigger a reorg after SVH
			// during the testReorgWalletBalance test.
			_, err = testutils.AdjustedSimnetMiner(miningNode.Node, 40)
			if err != nil {
				t.Fatalf("unable to generate initial blocks: %v", err)
			}

			// Setup a voting wallet for when the chain passes SVH.
			votingWallet, err := rpctest.NewVotingWallet(context.TODO(), miningNode)
			if err != nil {
				t.Fatalf("unable to create voting wallet: %v", err)
			}
			votingWallet.SetErrorReporting(func(err error) {
				t.Logf("Voting wallet error: %v", err)
			})
			votingWallet.SetMiner(func(ctx context.Context, nb uint32) ([]*chainhash.Hash, error) {
				return testutils.AdjustedSimnetMiner(miningNode.Node, nb)
			})
			if err = votingWallet.Start(); err != nil {
				t.Fatalf("unable to start voting wallet: %v", err)
			}
			defer votingWallet.Stop()

			rpcConfig := miningNode.RPCConfig()

			tempDir, err := ioutil.TempDir("", "channeldb")
			if err != nil {
				t.Fatalf("unable to create temp dir: %v", err)
			}
			db, err := channeldb.Open(tempDir)
			if err != nil {
				t.Fatalf("unable to create db: %v", err)
			}
			hintCacheCfg := chainntnfs.CacheConfig{
				QueryDisable: false,
			}
			hintCache, err := chainntnfs.NewHeightHintCache(hintCacheCfg, db)
			if err != nil {
				t.Fatalf("unable to create height hint cache: %v", err)
			}
			chainNotifier, err := dcrdnotify.New(
				&rpcConfig, netParams, hintCache, hintCache,
			)
			if err != nil {
				t.Fatalf("unable to create notifier: %v", err)
			}
			if err := chainNotifier.Start(); err != nil {
				t.Fatalf("unable to start notifier: %v", err)
			}

			if !runTests(t, walletDriver, backEnd, miningNode,
				rpcConfig, chainNotifier, votingWallet) {
				return
			}

			// Tear down this mining node so it won't interfere
			// with the next set of tests.
			cleanUpNode := miningNode
			miningNode = nil
			teardownErr := cleanUpNode.TearDown()

			// Copy the node logs from the original log dir.
			oldFileName := path.Join(minerLogDir, netParams.Name,
				"dcrd.log")
			newFileName := fmt.Sprintf("output-miner-%s-%s.log",
				walletDriver.WalletType, backEnd)
			err = os.Rename(oldFileName, newFileName)
			if err != nil {
				t.Logf("could not rename %s to %s: %v\n",
					oldFileName, newFileName, err)
			}

			if teardownErr != nil {
				t.Fatalf("unable to teardown rpc test harness: %v", err)
			}

			// Give enough time for all processes to end.
			time.Sleep(time.Second)
		}
	}
}

// runTests runs all of the tests for a single interface implementation and
// chain back-end combination. This makes it easier to use `defer` as well as
// factoring out the test logic from the loop which cycles through the
// interface implementations.
func runTests(t *testing.T, walletDriver *lnwallet.WalletDriver,
	backEnd string, miningNode *rpctest.Harness,
	rpcConfig rpcclient.ConnConfig,
	chainNotifier *dcrdnotify.DcrdNotifier,
	votingWallet *rpctest.VotingWallet) bool {
	var (
		aliceBio lnwallet.BlockChainIO
		bobBio   lnwallet.BlockChainIO

		aliceSigner input.Signer
		bobSigner   input.Signer

		aliceKeyRing keychain.SecretKeyRing
		bobKeyRing   keychain.SecretKeyRing

		aliceWalletController lnwallet.WalletController
		bobWalletController   lnwallet.WalletController
	)

	tempTestDirAlice, err := ioutil.TempDir("", "lnwallet")
	if err != nil {
		t.Fatalf("unable to create temp directory: %v", err)
	}
	t.Logf("Alice Dir: %s", tempTestDirAlice)
	//defer os.RemoveAll(tempTestDirAlice)

	tempTestDirBob, err := ioutil.TempDir("", "lnwallet")
	if err != nil {
		t.Fatalf("unable to create temp directory: %v", err)
	}
	t.Logf("Bob Dir: %s", tempTestDirBob)
	//defer os.RemoveAll(tempTestDirBob)

	aliceDBDir := filepath.Join(tempTestDirAlice, "cdb")
	aliceCDB, err := channeldb.Open(aliceDBDir)
	if err != nil {
		t.Fatalf("unable to open alice cdb: %v", err)
	}
	defer aliceCDB.Close()

	bobDBDir := filepath.Join(tempTestDirBob, "cdb")
	bobCDB, err := channeldb.Open(bobDBDir)
	if err != nil {
		t.Fatalf("unable to open bob cdb: %v", err)
	}
	defer bobCDB.Close()

	aliceSeed := sha256.New()
	aliceSeed.Write([]byte(backEnd))
	aliceSeed.Write([]byte(walletDriver.WalletType))
	aliceSeed.Write(aliceHDSeed[:])
	aliceSeedBytes := aliceSeed.Sum(nil)
	alicePrivatePass := []byte("alice-pass")

	bobSeed := sha256.New()
	bobSeed.Write([]byte(backEnd))
	bobSeed.Write([]byte(walletDriver.WalletType))
	bobSeed.Write(bobHDSeed[:])
	bobSeedBytes := bobSeed.Sum(nil)
	bobPrivatePass := []byte("bob-pass")

	walletType := walletDriver.WalletType
	switch walletType {
	case "dcrwallet":
		var aliceSyncer, bobSyncer dcrwallet.WalletSyncer
		switch backEnd {
		case "dcrd":
			aliceSyncer, err = dcrwallet.NewRPCSyncer(rpcConfig,
				netParams)
			if err != nil {
				t.Fatalf("unable to make chain rpc: %v", err)
			}
			bobSyncer, err = dcrwallet.NewRPCSyncer(rpcConfig,
				netParams)
			if err != nil {
				t.Fatalf("unable to make chain rpc: %v", err)
			}

			aliceBio, err = dcrwallet.NewRPCChainIO(rpcConfig, netParams)
			if err != nil {
				t.Fatalf("unable to make alice chain IO: %v", err)
			}

			bobBio, err = dcrwallet.NewRPCChainIO(rpcConfig, netParams)
			if err != nil {
				t.Fatalf("unable to make bob chain IO: %v", err)
			}
		case "spv":
			spvCfg := &dcrwallet.SPVSyncerConfig{
				Net:        netParams,
				Peers:      []string{miningNode.P2PAddress()},
				AppDataDir: tempTestDirAlice, // Safe to reuse since we use a fixed addr.
			}

			aliceSyncer, err = dcrwallet.NewSPVSyncer(spvCfg)
			if err != nil {
				t.Fatalf("unable to make alice spv syncer: %v", err)
			}
			bobSyncer, err = dcrwallet.NewSPVSyncer(spvCfg)
			if err != nil {
				t.Fatalf("unable to make bob spv syncer: %v", err)
			}
		default:
			t.Fatalf("unknown chain driver: %v", backEnd)
		}

		aliceWalletConfig := &dcrwallet.Config{
			PrivatePass: alicePrivatePass,
			HdSeed:      aliceSeedBytes,
			DataDir:     tempTestDirAlice,
			NetParams:   netParams,
			Syncer:      aliceSyncer,
			ChainIO:     aliceBio,
			DB:          aliceCDB,
		}
		aliceWalletController, err = walletDriver.New(aliceWalletConfig)
		if err != nil {
			t.Fatalf("unable to create alice wallet: %v", err)
		}
		aliceSigner = aliceWalletController.(*dcrwallet.DcrWallet)
		aliceKeyRing = aliceWalletController.(*dcrwallet.DcrWallet)

		bobWalletConfig := &dcrwallet.Config{
			PrivatePass: bobPrivatePass,
			HdSeed:      bobSeedBytes,
			DataDir:     tempTestDirBob,
			NetParams:   netParams,
			Syncer:      bobSyncer,
			ChainIO:     bobBio,
			DB:          bobCDB,
		}
		bobWalletController, err = walletDriver.New(bobWalletConfig)
		if err != nil {
			t.Fatalf("unable to create bob wallet: %v", err)
		}
		bobSigner = bobWalletController.(*dcrwallet.DcrWallet)
		bobKeyRing = bobWalletController.(*dcrwallet.DcrWallet)

		if backEnd == "spv" {
			aliceBio = aliceWalletController.(*dcrwallet.DcrWallet)
			bobBio = bobWalletController.(*dcrwallet.DcrWallet)
		}
	case "remotedcrwallet":
		var rpcSyncCfg *rpcclient.ConnConfig
		var spvSyncCfg *testutils.SPVConfig

		switch backEnd {
		case "dcrd":
			aliceBio, err = dcrwallet.NewRPCChainIO(rpcConfig,
				netParams)
			if err != nil {
				t.Fatalf("unable to make chain rpc: %v", err)
			}
			bobBio, err = dcrwallet.NewRPCChainIO(rpcConfig,
				netParams)
			if err != nil {
				t.Fatalf("unable to make chain rpc: %v", err)
			}

			rpcSyncCfg = &rpcConfig

		case "spv":
			spvSyncCfg = &testutils.SPVConfig{
				Address: miningNode.P2PAddress(),
			}

		default:
			t.Fatalf("unknown chain driver: %v", backEnd)
		}
		aliceConn, aliceCleanup := testutils.NewCustomTestRemoteDcrwallet(
			t, "Alice", tempTestDirAlice, aliceSeedBytes,
			alicePrivatePass, rpcSyncCfg, spvSyncCfg,
		)
		defer aliceCleanup()
		if err := testutils.SetPerAccountPassphrase(aliceConn, alicePrivatePass); err != nil {
			t.Fatalf("unable to set alice wallet to account pwd: %v", err)
		}
		aliceWalletConfig := &remotedcrwallet.Config{
			Conn:        aliceConn,
			ChainIO:     aliceBio,
			PrivatePass: alicePrivatePass,
			NetParams:   netParams,
			DB:          aliceCDB,
		}
		aliceWalletController, err = walletDriver.New(aliceWalletConfig)
		if err != nil {
			t.Fatalf("unable to create alice wallet: %v", err)
		}
		aliceSigner = aliceWalletController.(*remotedcrwallet.DcrWallet)
		aliceKeyRing = aliceWalletController.(*remotedcrwallet.DcrWallet)

		bobConn, bobCleanup := testutils.NewCustomTestRemoteDcrwallet(
			t, "Bob", tempTestDirBob, bobSeedBytes, bobPrivatePass,
			rpcSyncCfg, spvSyncCfg,
		)
		defer bobCleanup()
		if err := testutils.SetPerAccountPassphrase(bobConn, bobPrivatePass); err != nil {
			t.Fatalf("unable to set bob wallet to account pwd: %v", err)
		}
		bobWalletConfig := &remotedcrwallet.Config{
			Conn:        bobConn,
			ChainIO:     bobBio,
			PrivatePass: bobPrivatePass,
			NetParams:   netParams,
			DB:          bobCDB,
		}
		bobWalletController, err = walletDriver.New(bobWalletConfig)
		if err != nil {
			t.Fatalf("unable to create bob wallet: %v", err)
		}
		bobSigner = bobWalletController.(*remotedcrwallet.DcrWallet)
		bobKeyRing = bobWalletController.(*remotedcrwallet.DcrWallet)

		if backEnd == "spv" {
			aliceBio = aliceWalletController.(*remotedcrwallet.DcrWallet)
			bobBio = bobWalletController.(*remotedcrwallet.DcrWallet)
		}
	default:
		t.Fatalf("unknown wallet driver: %v", walletType)
	}

	// Funding via 20 outputs with 4DCR each.
	alice, err := createTestWallet(
		aliceCDB, miningNode, netParams,
		chainNotifier, aliceWalletController, aliceKeyRing,
		aliceSigner, aliceBio, votingWallet,
	)
	if err != nil {
		t.Fatalf("unable to create test ln wallet: %v", err)
	}
	defer alice.Shutdown()

	bob, err := createTestWallet(
		bobCDB, miningNode, netParams,
		chainNotifier, bobWalletController, bobKeyRing,
		bobSigner, bobBio, votingWallet,
	)
	if err != nil {
		t.Fatalf("unable to create test ln wallet: %v", err)
	}
	defer bob.Shutdown()

	// Wait for both wallets to be synced.
	timeout := time.After(time.Second * 120)
	aliceSync := aliceWalletController.InitialSyncChannel()
	bobSync := bobWalletController.InitialSyncChannel()
	for aliceSync != nil || bobSync != nil {
		select {
		case <-timeout:
			t.Fatalf("timeout while waiting for wallets to sync")
		case <-aliceSync:
			t.Logf("Alice synced")
			aliceSync = nil
		case <-bobSync:
			t.Logf("Bob synced")
			bobSync = nil
		}
	}

	// Load our test wallets with 20 outputs each holding 4DCR.
	if err := loadTestCredits(miningNode, alice, votingWallet.GenerateBlocks, 20, 4); err != nil {
		t.Logf("unable to send initial funds to alice: %v", err)
	}
	if err := loadTestCredits(miningNode, bob, votingWallet.GenerateBlocks, 20, 4); err != nil {
		t.Logf("unable to send initial funds to bob: %v", err)
	}

	t.Logf("Loaded test credits")

	// Both wallets should now have 80DCR available for
	// spending.
	assertProperBalance(t, alice, 1, 80)
	assertProperBalance(t, bob, 1, 80)

	// Execute every test, clearing possibly mutated
	// wallet state after each step.
	for _, walletTest := range walletTests {

		walletTest := walletTest

		testName := fmt.Sprintf("%v/%v:%v", walletType, backEnd,
			walletTest.name)
		success := t.Run(testName, func(t *testing.T) {
			if backEnd == "spv" &&
				strings.Contains(walletTest.name, "dual funder") {
				t.Skip("skipping dual funder tests for spv")
			}

			if backEnd == "spv" &&
				strings.Contains(walletTest.name, "reorg ") {
				t.Skip("skipping reorg tests for spv")
			}

			walletTest.test(miningNode, votingWallet, alice, bob, t)
		})
		if !success {
			return false
		}

		// TODO(roasbeef): possible reset mining
		// node's chainstate to initial level, cleanly
		// wipe buckets
		if err := clearWalletStates(alice, bob); err !=
			nil && err != kvdb.ErrBucketNotFound {
			t.Fatalf("unable to wipe wallet state: %v", err)
		}
	}

	return true
}
