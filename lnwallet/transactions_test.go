package lnwallet

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/decred/dcrd/blockchain/stake/v5"
	"github.com/decred/dcrd/blockchain/standalone/v2"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lntypes"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/shachain"
	"github.com/stretchr/testify/require"
)

/**
* This file implements that different types of transactions used in the
* lightning protocol are created correctly. To do so, the tests use the test
* vectors defined in Appendix B & C of BOLT 03.
 */

// testContext contains the test parameters defined in Appendix B & C of the
// BOLT 03 spec.
type testContext struct {
	localFundingPrivkey                *secp256k1.PrivateKey
	localPaymentBasepointSecret        *secp256k1.PrivateKey
	localDelayedPaymentBasepointSecret *secp256k1.PrivateKey
	remoteFundingPrivkey               *secp256k1.PrivateKey
	remoteRevocationBasepointSecret    *secp256k1.PrivateKey
	remotePaymentBasepointSecret       *secp256k1.PrivateKey

	localPerCommitSecret lntypes.Hash

	fundingTx *dcrutil.Tx

	localCsvDelay uint16
	fundingAmount dcrutil.Amount
	dustLimit     dcrutil.Amount
	commitHeight  uint64

	t      *testing.T
	params *chaincfg.Params
}

// newTestContext populates a new testContext struct with the constant
// parameters defined in the BOLT 03 spec.
func newTestContext(t *testing.T) (tc *testContext) {
	tc = new(testContext)

	priv := func(v string) *secp256k1.PrivateKey {
		k, err := privkeyFromHex(v)
		require.NoError(t, err)

		return k
	}

	tc.remoteFundingPrivkey = priv("1552dfba4f6cf29a62a0af13c8d6981d36d0ef8d61ba10fb0fe90da7634d7e13")
	tc.remoteRevocationBasepointSecret = priv("2222222222222222222222222222222222222222222222222222222222222222")
	tc.remotePaymentBasepointSecret = priv("4444444444444444444444444444444444444444444444444444444444444444")
	tc.localPaymentBasepointSecret = priv("1111111111111111111111111111111111111111111111111111111111111111")
	tc.localDelayedPaymentBasepointSecret = priv("3333333333333333333333333333333333333333333333333333333333333333")
	tc.localFundingPrivkey = priv("30ff4956bbdd3222d44cc5e8a1261dab1e07957bdac5ae88fe3261ef321f3749")

	var err error
	tc.localPerCommitSecret, err = lntypes.MakeHashFromStr("1f1e1d1c1b1a191817161514131211100f0e0d0c0b0a09080706050403020100")
	require.NoError(t, err)

	const fundingTxHex = "01000000010000000000000000000000000000000000000000000000000000000000000000ffffffff00ffffffff0100a0724e18090000000017a9142451a686d349c5ce6f72373ed59a841192d794418700000000000000000100e057eb481b000000000000ffffffff0800002f646372642f"

	tc.fundingTx, err = txFromHex(fundingTxHex)

	require.NoError(t, err)

	tc.localCsvDelay = 144
	tc.fundingAmount = 10000000
	tc.dustLimit = 546

	tc.params = chaincfg.RegNetParams()
	tc.commitHeight = 42
	tc.t = t

	return tc
}

var testHtlcs = []struct {
	incoming bool
	amount   lnwire.MilliAtom
	expiry   uint32
	preimage string
}{
	{
		incoming: true,
		amount:   1000000,
		expiry:   500,
		preimage: "0000000000000000000000000000000000000000000000000000000000000000",
	},
	{
		incoming: true,
		amount:   2000000,
		expiry:   501,
		preimage: "0101010101010101010101010101010101010101010101010101010101010101",
	},
	{
		incoming: false,
		amount:   2000000,
		expiry:   502,
		preimage: "0202020202020202020202020202020202020202020202020202020202020202",
	},
	{
		incoming: false,
		amount:   3000000,
		expiry:   503,
		preimage: "0303030303030303030303030303030303030303030303030303030303030303",
	},
	{
		incoming: true,
		amount:   4000000,
		expiry:   504,
		preimage: "0404040404040404040404040404040404040404040404040404040404040404",
	},
}

// htlcDesc is a description used to construct each HTLC in each test case.
type htlcDesc struct {
	RemoteSigHex    string
	ResolutionTxHex string
}

type testCase struct {
	Name          string
	LocalBalance  lnwire.MilliAtom
	RemoteBalance lnwire.MilliAtom
	FeePerKB      dcrutil.Amount

	// UseTestHtlcs defined whether the fixed set of test htlc should be
	// added to the channel before checking the commitment assertions.
	UseTestHtlcs bool

	HtlcDescs               []htlcDesc
	ExpectedCommitmentTxHex string
	RemoteSigHex            string
}

// TestCommitmentAndHTLCTransactions checks the test vectors specified in BOLT
// 03, Appendix C. This deterministically generates commitment and second level
// HTLC transactions and checks that they match the expected values.
func TestCommitmentAndHTLCTransactions(t *testing.T) {
	// These tests are based on the original Bitcoin LN ones and thus use
	// fees lower than the current fee floor. Thus we disable checking the
	// fee floor for these tests.
	disableFeeFloorCheck = true
	defer func() {
		disableFeeFloorCheck = false
	}()

	vectorSets := []struct {
		name     string
		jsonFile string
		chanType channeldb.ChannelType
	}{
		{
			name:     "legacy",
			chanType: channeldb.SingleFunderBit,
			jsonFile: "test_vectors_legacy.json",
		},
		{
			name:     "anchors",
			chanType: channeldb.SingleFunderTweaklessBit | channeldb.AnchorOutputsBit,
			jsonFile: "test_vectors_anchors.json",
		},
	}

	for _, set := range vectorSets {
		set := set

		var testCases []testCase

		jsonText, err := ioutil.ReadFile(set.jsonFile)
		require.NoError(t, err)

		err = json.Unmarshal(jsonText, &testCases)
		require.NoError(t, err)

		t.Run(set.name, func(t *testing.T) {
			for _, test := range testCases {
				test := test

				t.Run(test.Name, func(t *testing.T) {
					testVectors(t, set.chanType, test)
				})
			}
		})
	}
}

// addTestHtlcs adds the test vector htlcs to the update logs of the local and
// remote node.
func addTestHtlcs(t *testing.T, remote,
	local *LightningChannel) map[[20]byte]lntypes.Preimage {

	hash160map := make(map[[20]byte]lntypes.Preimage)
	for _, htlc := range testHtlcs {
		preimage, err := lntypes.MakePreimageFromStr(htlc.preimage)
		require.NoError(t, err)

		hash := preimage.Hash()

		// Store ripemd160 hash of the payment hash to later identify
		// resolutions.
		var hash160 [20]byte
		copy(hash160[:], input.Ripemd160H(hash[:]))
		hash160map[hash160] = preimage

		// Add htlc to the channel.
		chanID := lnwire.NewChanIDFromOutPoint(remote.ChanPoint)

		msg := &lnwire.UpdateAddHTLC{
			Amount:      htlc.amount,
			ChanID:      chanID,
			Expiry:      htlc.expiry,
			PaymentHash: hash,
		}
		if htlc.incoming {
			htlcID, err := remote.AddHTLC(msg, nil)
			require.NoError(t, err, "unable to add htlc")

			msg.ID = htlcID
			_, err = local.ReceiveHTLC(msg)
			require.NoError(t, err, "unable to recv htlc")
		} else {
			htlcID, err := local.AddHTLC(msg, nil)
			require.NoError(t, err, "unable to add htlc")

			msg.ID = htlcID
			_, err = remote.ReceiveHTLC(msg)
			require.NoError(t, err, "unable to recv htlc")
		}
	}

	return hash160map
}

// testVectors executes a commit dance to end up with the commitment transaction
// that is described in the test vectors and then asserts that all values are
// correct.
func testVectors(t *testing.T, chanType channeldb.ChannelType, test testCase) {
	tc := newTestContext(t)

	// Balances in the test vectors are before subtraction of in-flight
	// htlcs. Convert to spendable balances.
	remoteBalance := test.RemoteBalance
	localBalance := test.LocalBalance

	if test.UseTestHtlcs {
		for _, htlc := range testHtlcs {
			if htlc.incoming {
				remoteBalance += htlc.amount
			} else {
				localBalance += htlc.amount
			}
		}
	}

	// Set up a test channel on which the test commitment transaction is
	// going to be produced.
	remoteChannel, localChannel, cleanUp := createTestChannelsForVectors(
		tc,
		chanType, test.FeePerKB,
		remoteBalance.ToAtoms(),
		localBalance.ToAtoms(),
	)
	defer cleanUp()

	// Add htlcs (if any) to the update logs of both sides and save a hash
	// map that allows us to identify the htlcs in the scripts later on and
	// retrieve the corresponding preimage.
	var hash160map map[[20]byte]lntypes.Preimage
	if test.UseTestHtlcs {
		hash160map = addTestHtlcs(t, remoteChannel, localChannel)
	}

	// Execute commit dance to arrive at the point where the local node has
	// received the test commitment and the remote signature.
	localSig, localHtlcSigs, _, err := localChannel.SignNextCommitment()
	require.NoError(t, err, "local unable to sign commitment")

	err = remoteChannel.ReceiveNewCommitment(localSig, localHtlcSigs)
	require.NoError(t, err)

	revMsg, _, err := remoteChannel.RevokeCurrentCommitment()
	require.NoError(t, err)

	_, _, _, _, err = localChannel.ReceiveRevocation(revMsg)
	require.NoError(t, err)

	remoteSig, remoteHtlcSigs, _, err := remoteChannel.SignNextCommitment()
	require.NoError(t, err)

	err = localChannel.ReceiveNewCommitment(remoteSig, remoteHtlcSigs)
	require.NoError(t, err)

	_, _, err = localChannel.RevokeCurrentCommitment()
	require.NoError(t, err)

	// Now the local node force closes the channel so that we can inspect
	// its state.
	forceCloseSum, err := localChannel.ForceClose()
	require.NoError(t, err)

	// Uncomment the following line to debug the generated commitment tx.
	// t.Logf("XXXX %d %s\n%s", test.FeePerKB, test.Name, spew.Sdump(forceCloseSum.CloseTx))

	err = checkSignedCommitmentTxSanity(forceCloseSum.CloseTx, tc.fundingTx.MsgTx().TxOut[0], tc.params)
	require.NoError(t, err)

	err = checkSignedCommitmentTxSanity(forceCloseSum.CloseTx, tc.fundingTx.MsgTx().TxOut[0], tc.params)
	require.NoError(t, err)

	// Start verifying the signatures match.
	require.Equal(t, test.RemoteSigHex, hex.EncodeToString(remoteSig.ToSignatureBytes()),
		"commit tx remote sig incorrect")
	require.Equal(t, len(remoteHtlcSigs), len(test.HtlcDescs),
		"wrong number of htlc sigs")
	for i, sig := range remoteHtlcSigs {
		require.Equal(t, test.HtlcDescs[i].RemoteSigHex, hex.EncodeToString(sig.ToSignatureBytes()),
			"remote HTLC sig %d incorrect", i)
	}

	// Assert that the commitment transaction itself is as expected.
	var txBytes bytes.Buffer
	require.NoError(t, forceCloseSum.CloseTx.Serialize(&txBytes))
	require.Equal(t, test.ExpectedCommitmentTxHex, hex.EncodeToString(txBytes.Bytes()),
		"commitment tx hex incorrect")

	// Obtain the second level transactions that the local node's channel
	// state machine has produced. Store them in a map indexed by commit tx
	// output index. Also complete the second level transaction with the
	// preimage. This is normally done later in the contract resolver.
	secondLevelTxes := map[uint32]*wire.MsgTx{}
	storeTx := func(index uint32, tx *wire.MsgTx) {
		// Prevent overwrites.
		_, exists := secondLevelTxes[index]
		require.False(t, exists)

		secondLevelTxes[index] = tx
	}

	for _, r := range forceCloseSum.HtlcResolutions.IncomingHTLCs {
		successTx := r.SignedSuccessTx
		witnessStack, err := input.SigScriptToWitnessStack(successTx.TxIn[0].SignatureScript)
		require.NoError(t, err)
		witnessScript := witnessStack[3]
		var hash160 [20]byte
		copy(hash160[:], witnessScript[70:70+20])
		preimage, ok := hash160map[hash160]
		require.True(t, ok, "Could not find preimage for hash %x", hash160)
		witnessStack[2] = preimage[:]
		successTx.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(witnessStack)
		require.NoError(t, err)
		storeTx(r.HtlcPoint().Index, successTx)
	}
	for _, r := range forceCloseSum.HtlcResolutions.OutgoingHTLCs {
		storeTx(r.HtlcPoint().Index, r.SignedTimeoutTx)
	}

	// Create a list of second level transactions ordered by commit tx
	// output index.
	var keys []uint32
	for k := range secondLevelTxes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		return keys[a] < keys[b]
	})

	// Assert that this list matches the test vectors.
	failed := false
	for i, idx := range keys {
		tx := secondLevelTxes[idx]
		var b bytes.Buffer
		err := tx.Serialize(&b)
		require.NoError(t, err)

		err = checkSignedCommitmentSpendingTxSanity(tx, forceCloseSum.CloseTx,
			tc.params)
		require.NoError(t, err)

		require.Equal(
			t,
			test.HtlcDescs[i].ResolutionTxHex,
			hex.EncodeToString(b.Bytes()),
			"second level tx %d incorrect", i,
		)
	}
	if failed {
		t.Fatalf("failed")
	}
}

func TestCommitTxStateHint(t *testing.T) {
	t.Parallel()

	stateHintTests := []struct {
		name       string
		from       uint64
		to         uint64
		inputs     int
		shouldFail bool
	}{
		{
			name:       "states 0 to 1000",
			from:       0,
			to:         1000,
			inputs:     1,
			shouldFail: false,
		},
		{
			name:       "states 'maxStateHint-1000' to 'maxStateHint'",
			from:       maxStateHint - 1000,
			to:         maxStateHint,
			inputs:     1,
			shouldFail: false,
		},
		{
			name:       "state 'maxStateHint+1'",
			from:       maxStateHint + 1,
			to:         maxStateHint + 10,
			inputs:     1,
			shouldFail: true,
		},
		{
			name:       "commit transaction with two inputs",
			inputs:     2,
			shouldFail: true,
		},
	}

	var obfuscator [StateHintSize]byte
	copy(obfuscator[:], testHdSeed[:StateHintSize])
	timeYesterday := uint32(time.Now().Unix() - 24*60*60)

	for _, test := range stateHintTests {
		commitTx := wire.NewMsgTx()
		commitTx.Version = input.LNTxVersion

		// Add supplied number of inputs to the commitment transaction.
		for i := 0; i < test.inputs; i++ {
			commitTx.AddTxIn(&wire.TxIn{})
		}

		for i := test.from; i <= test.to; i++ {
			stateNum := i

			err := SetStateNumHint(commitTx, stateNum, obfuscator)
			if err != nil && !test.shouldFail {
				t.Fatalf("unable to set state num %v: %v", i, err)
			} else if err == nil && test.shouldFail {
				t.Fatalf("Failed(%v): test should fail but did not", test.name)
			}

			locktime := commitTx.LockTime
			sequence := commitTx.TxIn[0].Sequence

			// Locktime should not be less than 500,000,000 and not larger
			// than the time 24 hours ago. One day should provide a good
			// enough buffer for the tests.
			if locktime < 5e8 || locktime > timeYesterday {
				if !test.shouldFail {
					t.Fatalf("The value of locktime (%v) may cause the commitment "+
						"transaction to be unspendable", locktime)
				}
			}

			if sequence&wire.SequenceLockTimeDisabled == 0 {
				if !test.shouldFail {
					t.Fatalf("Sequence locktime is NOT disabled when it should be")
				}
			}

			extractedStateNum := GetStateNumHint(commitTx, obfuscator)
			if extractedStateNum != stateNum && !test.shouldFail {
				t.Fatalf("state number mismatched, expected %v, got %v",
					stateNum, extractedStateNum)
			} else if extractedStateNum == stateNum && test.shouldFail {
				t.Fatalf("Failed(%v): test should fail but did not", test.name)
			}
		}
		t.Logf("Passed: %v", test.name)
	}
}

// testSpendValidation ensures that we're able to spend all outputs in the
// commitment transaction that we create.
func testSpendValidation(t *testing.T, tweakless bool) {
	// We generate a fake output, and the corresponding txin. This output
	// doesn't need to exist, as we'll only be validating spending from the
	// transaction that references this.
	txid, err := chainhash.NewHash(testHdSeed.CloneBytes())
	if err != nil {
		t.Fatalf("unable to create txid: %v", err)
	}
	fundingOut := &wire.OutPoint{
		Hash:  *txid,
		Index: 50,
		Tree:  wire.TxTreeRegular,
	}

	const channelBalance = dcrutil.Amount(1 * 10e8)
	const csvTimeout = 5
	fakeFundingTxIn := wire.NewTxIn(fundingOut, int64(channelBalance), nil)

	// We also set up set some resources for the commitment transaction.
	// Each side currently has 1 DCR within the channel, with a total
	// channel capacity of 2 DCR.
	aliceKeyPriv, aliceKeyPub := privKeyFromBytes(testWalletPrivKey)
	bobKeyPriv, bobKeyPub := privKeyFromBytes(bobsPrivKey)

	revocationPreimage := testHdSeed.CloneBytes()
	commitSecret, commitPoint := privKeyFromBytes(revocationPreimage)
	revokePubKey := input.DeriveRevocationPubkey(bobKeyPub, commitPoint)

	aliceDelayKey := input.TweakPubKey(aliceKeyPub, commitPoint)

	// Bob will have the channel "force closed" on him, so for the sake of
	// our commitments, if it's tweakless, his key will just be his regular
	// pubkey.
	bobPayKey := input.TweakPubKey(bobKeyPub, commitPoint)
	channelType := channeldb.SingleFunderBit
	if tweakless {
		bobPayKey = bobKeyPub
		channelType = channeldb.SingleFunderTweaklessBit
	}

	remoteCommitTweak := input.SingleTweakBytes(commitPoint, aliceKeyPub)
	localCommitTweak := input.SingleTweakBytes(commitPoint, bobKeyPub)

	aliceSelfOutputSigner := &input.MockSigner{
		Privkeys: []*secp256k1.PrivateKey{aliceKeyPriv},
	}

	// Calculate the dust limit we'll use for the test.
	dustLimit := DustLimitForSize(input.P2PKHPkScriptSize)

	aliceChanCfg := &channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit: dustLimit,
			CsvDelay:  csvTimeout,
		},
	}

	bobChanCfg := &channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit: dustLimit,
			CsvDelay:  csvTimeout,
		},
	}

	// With all the test data set up, we create the commitment transaction.
	// We only focus on a single party's transactions, as the scripts are
	// identical with the roles reversed.
	//
	// This is Alice's commitment transaction, so she must wait a CSV delay
	// of 5 blocks before sweeping the output, while bob can spend
	// immediately with either the revocation key, or his regular key.
	keyRing := &CommitmentKeyRing{
		ToLocalKey:    aliceDelayKey,
		RevocationKey: revokePubKey,
		ToRemoteKey:   bobPayKey,
	}
	commitmentTx, err := CreateCommitTx(
		channelType, *fakeFundingTxIn, keyRing, aliceChanCfg,
		bobChanCfg, channelBalance, channelBalance, 0,
	)
	if err != nil {
		t.Fatalf("unable to create commitment transaction: %v", nil)
	}

	delayOutput := commitmentTx.TxOut[0]
	regularOutput := commitmentTx.TxOut[1]

	// We're testing an uncooperative close, output sweep, so construct a
	// transaction which sweeps the funds to a random address.
	targetOutput, err := input.CommitScriptUnencumbered(aliceKeyPub)
	if err != nil {
		t.Fatalf("unable to create target output: %v", err)
	}
	sweepTx := wire.NewMsgTx()
	sweepTx.Version = input.LNTxVersion
	sweepTx.AddTxIn(wire.NewTxIn(&wire.OutPoint{
		Hash:  commitmentTx.TxHash(),
		Index: 0,
	}, delayOutput.Value, nil))
	sweepTx.AddTxOut(&wire.TxOut{
		PkScript: targetOutput,
		Value:    0.5 * 10e8,
	})

	// First, we'll test spending with Alice's key after the timeout.
	delayScript, err := input.CommitScriptToSelf(
		csvTimeout, aliceDelayKey, revokePubKey,
	)
	if err != nil {
		t.Fatalf("unable to generate alice delay script: %v", err)
	}
	sweepTx.TxIn[0].Sequence = input.LockTimeToSequence(false, csvTimeout)
	signDesc := &input.SignDescriptor{
		WitnessScript: delayScript,
		KeyDesc: keychain.KeyDescriptor{
			PubKey: aliceKeyPub,
		},
		SingleTweak: remoteCommitTweak,
		Output: &wire.TxOut{
			Value: int64(channelBalance),
		},
		HashType:   txscript.SigHashAll,
		InputIndex: 0,
	}
	aliceWitnessSpend, err := input.CommitSpendTimeout(
		aliceSelfOutputSigner, signDesc, sweepTx,
	)
	if err != nil {
		t.Fatalf("unable to generate delay commit spend witness: %v", err)
	}
	sweepTx.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(aliceWitnessSpend)
	if err != nil {
		t.Fatalf("unable to convert witness stack to sigScript: %v", err)
	}

	vm, err := txscript.NewEngine(delayOutput.PkScript,
		sweepTx, 0, input.ScriptVerifyFlags, delayOutput.Version, nil)
	if err != nil {
		t.Fatalf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("spend from delay output is invalid: %v", err)
	}

	localSigner := &input.MockSigner{Privkeys: []*secp256k1.PrivateKey{bobKeyPriv}}

	// Next, we'll test bob spending with the derived revocation key to
	// simulate the scenario when Alice broadcasts this commitment
	// transaction after it's been revoked.
	signDesc = &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		DoubleTweak:   commitSecret,
		WitnessScript: delayScript,
		Output: &wire.TxOut{
			Value: int64(channelBalance),
		},
		HashType:   txscript.SigHashAll,
		InputIndex: 0,
	}
	bobWitnessSpend, err := input.CommitSpendRevoke(localSigner, signDesc,
		sweepTx)
	if err != nil {
		t.Fatalf("unable to generate revocation witness: %v", err)
	}
	sweepTx.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(bobWitnessSpend)
	if err != nil {
		t.Fatalf("unable to convert witness stack to sigScript: %v", err)
	}

	vm, err = txscript.NewEngine(delayOutput.PkScript,
		sweepTx, 0, input.ScriptVerifyFlags, delayOutput.Version, nil)
	if err != nil {
		t.Fatalf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("revocation spend is invalid: %v", err)
	}

	// In order to test the final scenario, we modify the TxIn of the sweep
	// transaction to instead point to the regular output (non delay)
	// within the commitment transaction.
	sweepTx.TxIn[0] = &wire.TxIn{
		PreviousOutPoint: wire.OutPoint{
			Hash:  commitmentTx.TxHash(),
			Index: 1,
		},
	}

	// Finally, we test bob sweeping his output as normal in the case that
	// Alice broadcasts this commitment transaction.
	bobScriptP2PKH, err := input.CommitScriptUnencumbered(bobPayKey)
	if err != nil {
		t.Fatalf("unable to create bob p2wkh script: %v", err)
	}
	signDesc = &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			PubKey: bobKeyPub,
		},
		WitnessScript: bobScriptP2PKH,
		Output: &wire.TxOut{
			Value:    int64(channelBalance),
			PkScript: bobScriptP2PKH,
		},
		HashType:   txscript.SigHashAll,
		InputIndex: 0,
	}
	if !tweakless {
		signDesc.SingleTweak = localCommitTweak
	}
	bobRegularSpend, err := input.CommitSpendNoDelay(
		localSigner, signDesc, sweepTx, tweakless,
	)
	if err != nil {
		t.Fatalf("unable to create bob regular spend: %v", err)
	}
	sweepTx.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(bobRegularSpend)
	if err != nil {
		t.Fatalf("unable to convert witness stack to sigScript: %v", err)
	}

	vm, err = txscript.NewEngine(regularOutput.PkScript,
		sweepTx, 0, input.ScriptVerifyFlags, regularOutput.Version, nil)
	if err != nil {
		t.Fatalf("unable to create engine: %v", err)
	}
	if err := vm.Execute(); err != nil {
		t.Fatalf("bob p2wkh spend is invalid: %v", err)
	}
}

// TestCommitmentSpendValidation test the spendability of both outputs within
// the commitment transaction.
//
// The following spending cases are covered by this test:
//   - Alice's spend from the delayed output on her commitment transaction.
//   - Bob's spend from Alice's delayed output when she broadcasts a revoked
//     commitment transaction.
//   - Bob's spend from his unencumbered output within Alice's commitment
//     transaction.
func TestCommitmentSpendValidation(t *testing.T) {
	t.Parallel()

	// In the modern network, all channels use the new tweakless format,
	// but we also need to support older nodes that want to open channels
	// with the legacy format, so we'll test spending in both scenarios.
	for _, tweakless := range []bool{true, false} {
		tweakless := tweakless
		t.Run(fmt.Sprintf("tweak=%v", tweakless), func(t *testing.T) {
			testSpendValidation(t, tweakless)
		})
	}
}

type mockProducer struct {
	secret shachain.ShaHash
}

func (p *mockProducer) AtIndex(uint64) (*shachain.ShaHash, error) {
	return &p.secret, nil
}

func (p *mockProducer) Encode(w io.Writer) error {
	_, err := w.Write(p.secret[:])
	return err
}

// createTestChannelsForVectors creates two LightningChannel instances for the
// test channel that is used to verify the test vectors.
func createTestChannelsForVectors(tc *testContext, chanType channeldb.ChannelType,
	feeRate dcrutil.Amount, remoteBalance, localBalance dcrutil.Amount) (
	*LightningChannel, *LightningChannel, func()) {

	t := tc.t

	prevOut := &wire.OutPoint{
		Hash:  *tc.fundingTx.Hash(),
		Index: 0,
	}

	fundingTxIn := wire.NewTxIn(prevOut, 0, nil)

	// Generate random some keys that don't actually matter but need to be
	// set.
	var (
		remoteDummy1, remoteDummy2 *secp256k1.PrivateKey
		localDummy2, localDummy1   *secp256k1.PrivateKey
	)
	generateKeys := []**secp256k1.PrivateKey{
		&remoteDummy1, &remoteDummy2, &localDummy1, &localDummy2,
	}
	for _, keyRef := range generateKeys {
		privkey, err := secp256k1.GeneratePrivateKey()
		require.NoError(t, err)
		*keyRef = privkey
	}

	// Define channel configurations.
	remoteCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit: tc.dustLimit,
			MaxPendingAmount: lnwire.NewMAtomsFromAtoms(
				tc.fundingAmount,
			),
			ChanReserve:      0,
			MinHTLC:          0,
			MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
			CsvDelay:         tc.localCsvDelay,
		},
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: tc.remoteFundingPrivkey.PubKey(),
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: tc.remotePaymentBasepointSecret.PubKey(),
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: tc.remotePaymentBasepointSecret.PubKey(),
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: remoteDummy1.PubKey(),
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: tc.remoteRevocationBasepointSecret.PubKey(),
		},
	}
	localCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit: tc.dustLimit,
			MaxPendingAmount: lnwire.NewMAtomsFromAtoms(
				tc.fundingAmount,
			),
			ChanReserve:      0,
			MinHTLC:          0,
			MaxAcceptedHtlcs: input.MaxHTLCNumber / 2,
			CsvDelay:         tc.localCsvDelay,
		},
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: tc.localFundingPrivkey.PubKey(),
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: tc.localPaymentBasepointSecret.PubKey(),
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: tc.localPaymentBasepointSecret.PubKey(),
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: tc.localDelayedPaymentBasepointSecret.PubKey(),
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: localDummy1.PubKey(),
		},
	}

	// Create mock producers to force usage of the test vector commitment
	// point.
	remotePreimageProducer := &mockProducer{
		secret: shachain.ShaHash(tc.localPerCommitSecret),
	}
	remoteCommitPoint := input.ComputeCommitmentPoint(
		tc.localPerCommitSecret[:],
	)

	localPreimageProducer := &mockProducer{
		secret: shachain.ShaHash(tc.localPerCommitSecret),
	}
	localCommitPoint := input.ComputeCommitmentPoint(
		tc.localPerCommitSecret[:],
	)

	// Create temporary databases.
	remotePath, err := ioutil.TempDir("", "remotedb")
	require.NoError(t, err)

	dbRemote, err := channeldb.Open(remotePath)
	require.NoError(t, err)

	localPath, err := ioutil.TempDir("", "localdb")
	require.NoError(t, err)

	dbLocal, err := channeldb.Open(localPath)
	require.NoError(t, err)

	// Create the initial commitment transactions for the channel.
	feePerKB := chainfee.AtomPerKByte(feeRate)
	commitSize := input.CommitmentTxSize
	if chanType.HasAnchors() {
		commitSize = input.CommitmentWithAnchorsTxSize
	}
	commitFee := feePerKB.FeeForSize(commitSize)

	var anchorAmt dcrutil.Amount
	if chanType.HasAnchors() {
		anchorAmt = 2 * anchorSize
	}

	remoteCommitTx, localCommitTx, err := CreateCommitmentTxns(
		remoteBalance, localBalance-commitFee,
		&remoteCfg, &localCfg, remoteCommitPoint,
		localCommitPoint, *fundingTxIn, chanType,
		tc.params,
	)
	require.NoError(t, err)

	// Set up the full channel state.

	// Subtract one because extra sig exchange will take place during setup
	// to get to the right test point.
	var commitHeight = tc.commitHeight - 1

	remoteCommit := channeldb.ChannelCommitment{
		CommitHeight:  commitHeight,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(remoteBalance),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(localBalance - commitFee - anchorAmt),
		CommitFee:     commitFee,
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitTx:      remoteCommitTx,
		CommitSig:     testSigBytes,
	}
	localCommit := channeldb.ChannelCommitment{
		CommitHeight:  commitHeight,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(localBalance - commitFee - anchorAmt),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(remoteBalance),
		CommitFee:     commitFee,
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitTx:      localCommitTx,
		CommitSig:     testSigBytes,
	}

	var chanIDBytes [8]byte
	_, err = io.ReadFull(rand.Reader, chanIDBytes[:])
	require.NoError(t, err)

	shortChanID := lnwire.NewShortChanIDFromInt(
		binary.BigEndian.Uint64(chanIDBytes[:]),
	)

	remoteChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            remoteCfg,
		RemoteChanCfg:           localCfg,
		IdentityPub:             remoteDummy2.PubKey(),
		FundingOutpoint:         *prevOut,
		ShortChannelID:          shortChanID,
		ChanType:                chanType,
		IsInitiator:             false,
		Capacity:                tc.fundingAmount,
		RemoteCurrentRevocation: localCommitPoint,
		RevocationProducer:      remotePreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         remoteCommit,
		RemoteCommitment:        remoteCommit,
		Db:                      dbRemote,
		Packager:                channeldb.NewChannelPackager(shortChanID),
		FundingTxn:              tc.fundingTx.MsgTx(),
	}
	localChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            localCfg,
		RemoteChanCfg:           remoteCfg,
		IdentityPub:             localDummy2.PubKey(),
		FundingOutpoint:         *prevOut,
		ShortChannelID:          shortChanID,
		ChanType:                chanType,
		IsInitiator:             true,
		Capacity:                tc.fundingAmount,
		RemoteCurrentRevocation: remoteCommitPoint,
		RevocationProducer:      localPreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         localCommit,
		RemoteCommitment:        localCommit,
		Db:                      dbLocal,
		Packager:                channeldb.NewChannelPackager(shortChanID),
		FundingTxn:              tc.fundingTx.MsgTx(),
	}

	// Create mock signers that can sign for the keys that are used.
	localSigner := &input.MockSigner{Privkeys: []*secp256k1.PrivateKey{
		tc.localPaymentBasepointSecret, tc.localDelayedPaymentBasepointSecret,
		tc.localFundingPrivkey, localDummy1, localDummy2,
	}}

	remoteSigner := &input.MockSigner{Privkeys: []*secp256k1.PrivateKey{
		tc.remoteFundingPrivkey, tc.remoteRevocationBasepointSecret,
		tc.remotePaymentBasepointSecret, remoteDummy1, remoteDummy2,
	}}

	remotePool := NewSigPool(1, remoteSigner)
	channelRemote, err := NewLightningChannel(
		remoteSigner, remoteChannelState, remotePool, tc.params,
	)
	require.NoError(t, err)
	require.NoError(t, remotePool.Start())

	localPool := NewSigPool(1, localSigner)
	channelLocal, err := NewLightningChannel(
		localSigner, localChannelState, localPool, tc.params,
	)
	require.NoError(t, err)
	require.NoError(t, localPool.Start())

	// Create state hunt obfuscator for the commitment transaction.
	obfuscator := createStateHintObfuscator(remoteChannelState)
	err = SetStateNumHint(
		remoteCommitTx, commitHeight, obfuscator,
	)
	require.NoError(t, err)

	err = SetStateNumHint(
		localCommitTx, commitHeight, obfuscator,
	)
	require.NoError(t, err)

	// Initialize the database.
	addr := &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 18556,
	}
	require.NoError(t, channelRemote.channelState.SyncPending(addr, 101))

	addr = &net.TCPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 18555,
	}
	require.NoError(t, channelLocal.channelState.SyncPending(addr, 101))

	// Now that the channel are open, simulate the start of a session by
	// having local and remote extend their revocation windows to each other.
	err = initRevocationWindows(channelRemote, channelLocal)
	require.NoError(t, err)

	// Return a clean up function that stops goroutines and removes the test
	// databases.
	cleanUpFunc := func() {
		dbLocal.Close()
		dbRemote.Close()

		os.RemoveAll(localPath)
		os.RemoveAll(remotePath)

		require.NoError(t, remotePool.Stop())
		require.NoError(t, localPool.Stop())
	}

	return channelRemote, channelLocal, cleanUpFunc
}

// checkLnTransactionSanity checks whether an ln transaction (funding,
// commitment, etc) is reasonably sane according to consensus and standardness
// checks that don't require a full backing blockchain to verify.
func checkLnTransactionSanity(tx *wire.MsgTx, utxos map[wire.OutPoint]*wire.TxOut, netParams *chaincfg.Params) error {
	err := standalone.CheckTransactionSanity(tx, uint64(netParams.MaxTxSize))
	if err != nil {
		return fmt.Errorf("error checking tx sanity: %v", err)
	}

	var inputSum int64
	var outputSum int64

	txType := stake.DetermineTxType(tx)
	if txType != stake.TxTypeRegular {
		return fmt.Errorf("transaction is not of the regular type")
	}
	if tx.Expiry != wire.NoExpiryValue {
		return fmt.Errorf("expiry for the tx is not %d", wire.NoExpiryValue)
	}
	if tx.Version != input.LNTxVersion {
		return fmt.Errorf("tx version (%d) different than expected (%d)",
			tx.Version, input.LNTxVersion)
	}
	for i, out := range tx.TxOut {
		if out.Version != scriptVersion {
			return fmt.Errorf("output %d of tx does not use the "+
				"default script version (found %d)", i, out.Version)
		}

		outputSum += out.Value
	}
	for i, in := range tx.TxIn {
		utxo, hasUtxo := utxos[in.PreviousOutPoint]
		if !hasUtxo {
			return fmt.Errorf("utxo for input %d (%s) of tx not provided", i,
				in.PreviousOutPoint)
		}

		engine, err := txscript.NewEngine(utxo.PkScript, tx, i,
			input.ScriptVerifyFlags, utxo.Version, nil)
		if err != nil {
			return fmt.Errorf("error creating engine to process input %d: %v",
				i, err)
		}

		err = engine.Execute()
		if err != nil {
			return fmt.Errorf("error executing script of input %d: %v", i, err)
		}

		inputSum += utxo.Value
	}

	if (outputSum > inputSum) || (outputSum < 0) {
		return fmt.Errorf("sum of output amounts > sum of input amounts")
	}

	return nil
}

// checkSignedCommitmentTxSanity checks whether a commitment transaction is
// reasonably sane according to consensus and standardness checks that don't
// require a full backing blockchain to verify.
//
// It assumes the commit transaction input previous outpoint is correctly
// pointing to the passed fundingTxOut.
func checkSignedCommitmentTxSanity(commitTx *wire.MsgTx, fundingTxOut *wire.TxOut, netParams *chaincfg.Params) error {

	if len(commitTx.TxIn) != 1 {
		return fmt.Errorf("commit transaction has invalid number of inputs")
	}

	utxos := make(map[wire.OutPoint]*wire.TxOut, 1)
	utxos[commitTx.TxIn[0].PreviousOutPoint] = fundingTxOut

	return checkLnTransactionSanity(commitTx, utxos, netParams)
}

// checkSignedCommitmentSpendingTxSanity checks whether a transaction spending
// from a commitment transaction (eg: an htlc resolution transaction or a breach
// remedy transaction ) is reasonably sane according to consensus and
// standardness checks that don't require a full backing blockchain to verify.
//
// This assumes the commitment transaction is sane (ie
// checkSignedCommitmentTxSanity returns nil).
func checkSignedCommitmentSpendingTxSanity(spendTx, commitTx *wire.MsgTx, netParams *chaincfg.Params) error {

	commitTxHash := commitTx.TxHash()
	countCommitOuts := uint32(len(commitTx.TxOut))

	utxos := make(map[wire.OutPoint]*wire.TxOut, len(spendTx.TxIn))
	for i, in := range spendTx.TxIn {
		outp := in.PreviousOutPoint
		if (outp.Hash != commitTxHash) || (outp.Index >= countCommitOuts) ||
			(outp.Tree != wire.TxTreeRegular) {

			return fmt.Errorf("input %d of spender tx does not spend from "+
				"commit tx", i)
		}
		utxos[outp] = commitTx.TxOut[outp.Index]
	}

	return checkLnTransactionSanity(spendTx, utxos, netParams)
}
