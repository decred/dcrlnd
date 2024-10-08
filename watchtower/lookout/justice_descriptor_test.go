package lookout_test

import (
	"testing"
	"time"

	"github.com/decred/dcrd/blockchain/standalone/v2"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/dcrutil/v4/txsort"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/watchtower/blob"
	"github.com/decred/dcrlnd/watchtower/lookout"
	"github.com/decred/dcrlnd/watchtower/wtdb"
	"github.com/decred/dcrlnd/watchtower/wtmock"
	"github.com/decred/dcrlnd/watchtower/wtpolicy"
	"github.com/stretchr/testify/require"
)

const csvDelay uint32 = 144

var (
	revPrivBytes = []byte{
		0x8f, 0x4b, 0x51, 0x83, 0xa9, 0x34, 0xbd, 0x5f,
		0x74, 0x6c, 0x9d, 0x5c, 0xae, 0x88, 0x2d, 0x31,
		0x06, 0x90, 0xdd, 0x8c, 0x9b, 0x31, 0xbc, 0xd1,
		0x78, 0x91, 0x88, 0x2a, 0xf9, 0x74, 0xa0, 0xef,
	}

	toLocalPrivBytes = []byte{
		0xde, 0x17, 0xc1, 0x2f, 0xdc, 0x1b, 0xc0, 0xc6,
		0x59, 0x5d, 0xf9, 0xc1, 0x3e, 0x89, 0xbc, 0x6f,
		0x01, 0x85, 0x45, 0x76, 0x26, 0xce, 0x9c, 0x55,
		0x3b, 0xc9, 0xec, 0x3d, 0xd8, 0x8b, 0xac, 0xa8,
	}

	toRemotePrivBytes = []byte{
		0x28, 0x59, 0x6f, 0x36, 0xb8, 0x9f, 0x19, 0x5d,
		0xcb, 0x07, 0x48, 0x8a, 0xe5, 0x89, 0x71, 0x74,
		0x70, 0x4c, 0xff, 0x1e, 0x9c, 0x00, 0x93, 0xbe,
		0xe2, 0x2e, 0x68, 0x08, 0x4c, 0xb4, 0x0f, 0x4f,
	}

	rewardCommitType = blob.TypeFromFlags(
		blob.FlagReward, blob.FlagCommitOutputs,
	)

	altruistCommitType = blob.FlagCommitOutputs.Type()

	altruistAnchorCommitType = blob.TypeAltruistAnchorCommit
)

// TestJusticeDescriptor asserts that a JusticeDescriptor is able to produce the
// correct justice transaction for different blob types.
func TestJusticeDescriptor(t *testing.T) {
	tests := []struct {
		name     string
		blobType blob.Type
	}{
		{
			name:     "reward and commit type",
			blobType: rewardCommitType,
		},
		{
			name:     "altruist and commit type",
			blobType: altruistCommitType,
		},
		{
			name:     "altruist anchor commit type",
			blobType: altruistAnchorCommitType,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testJusticeDescriptor(t, test.blobType)
		})
	}
}

func privKeyFromBytes(b []byte) (*secp256k1.PrivateKey, *secp256k1.PublicKey) {
	p := secp256k1.PrivKeyFromBytes(b)
	return p, p.PubKey()
}

func testJusticeDescriptor(t *testing.T, blobType blob.Type) {
	isAnchorChannel := blobType.IsAnchorChannel()

	const (
		localAmount  = dcrutil.Amount(100000)
		remoteAmount = dcrutil.Amount(200000)
		totalAmount  = localAmount + remoteAmount
	)
	netParams := chaincfg.RegNetParams()

	// Parse the key pairs for all keys used in the test.
	revSK, revPK := privKeyFromBytes(revPrivBytes)
	_, toLocalPK := privKeyFromBytes(toLocalPrivBytes)
	toRemoteSK, toRemotePK := privKeyFromBytes(toRemotePrivBytes)

	// Create the signer, and add the revocation and to-remote privkeys.
	signer := wtmock.NewMockSigner()
	var (
		revKeyLoc      = signer.AddPrivKey(revSK)
		toRemoteKeyLoc = signer.AddPrivKey(toRemoteSK)
	)

	// Construct the to-local witness script.
	toLocalScript, err := input.CommitScriptToSelf(
		csvDelay, toLocalPK, revPK,
	)
	require.Nil(t, err)

	// Compute the to-local witness script hash.
	toLocalScriptHash, err := input.ScriptHashPkScript(toLocalScript)
	require.Nil(t, err)

	// Compute the to-remote redeem script, pk script, and sequence
	// numbers.
	//
	// NOTE: This is pretty subtle.
	//
	// The actual redeem script (i.e. last push of the SigScript) for a
	// p2pkh output is just the pubkey, but the witness sighash calculation
	// injects the classic p2kh script: OP_DUP OP_HASH160 <pubkey-hash160>
	// OP_EQUALVERIFY OP_CHECKSIG. When signing for p2pkh we don't pass the
	// raw pubkey as the redeem script to the sign descriptor (since that's
	// also not a valid script).  Instead we give it the _p2pkh redeem
	// script_ of the standard form (OP_DUP, ...) from which pubkey-hash160
	// is extracted during sighash calculation.
	//
	// On the other hand, signing for the anchor P2SH to-remote outputs
	// requires the sign descriptor to contain the redeem script verbatim.
	// This difference in behavior forces us to use a distinct
	// toRemoteSigningScript to handle both cases.
	var (
		toRemoteSequence      uint32
		toRemoteRedeemScript  []byte
		toRemoteScriptHash    []byte
		toRemoteSigningScript []byte
	)
	if isAnchorChannel {
		toRemoteSequence = 1
		toRemoteRedeemScript, err = input.CommitScriptToRemoteConfirmed(
			toRemotePK,
		)
		require.Nil(t, err)

		toRemoteScriptHash, err = input.ScriptHashPkScript(
			toRemoteRedeemScript,
		)
		require.Nil(t, err)

		// As it should be.
		toRemoteSigningScript = toRemoteRedeemScript
	} else {
		toRemoteRedeemScript = toRemotePK.SerializeCompressed()
		toRemoteScriptHash, err = input.CommitScriptUnencumbered(
			toRemotePK,
		)
		require.Nil(t, err)

		// NOTE: This is the _pkscript_.
		toRemoteSigningScript = toRemoteScriptHash
	}

	// Construct the breaching commitment txn, containing the to-local and
	// to-remote outputs. We don't need any inputs for this test.
	breachTxn := &wire.MsgTx{
		Version: 2,
		TxIn:    []*wire.TxIn{},
		TxOut: []*wire.TxOut{
			{
				Value:    int64(localAmount),
				PkScript: toLocalScriptHash,
			},
			{
				Value:    int64(remoteAmount),
				PkScript: toRemoteScriptHash,
			},
		},
	}
	breachTxID := breachTxn.TxHash()

	// Compute the size estimate for our justice transaction.
	var sizeEstimate input.TxSizeEstimator
	sizeEstimate.AddCustomInput(input.ToLocalPenaltySigScriptSize)

	if isAnchorChannel {
		sizeEstimate.AddCustomInput(input.ToRemoteConfirmedWitnessSize)
	} else {
		sizeEstimate.AddP2PKHInput()
	}
	sizeEstimate.AddP2PKHOutput()
	if blobType.Has(blob.FlagReward) {
		sizeEstimate.AddP2PKHOutput()
	}
	txSize := sizeEstimate.Size()

	// Create a session info so that simulate agreement of the sweep
	// parameters that should be used in constructing the justice
	// transaction.
	policy := wtpolicy.Policy{
		TxPolicy: wtpolicy.TxPolicy{
			BlobType:     blobType,
			SweepFeeRate: 2000,
			RewardRate:   900000,
		},
	}
	sessionInfo := &wtdb.SessionInfo{
		Policy:        policy,
		RewardAddress: makeRandomP2PKHPkScript(),
	}

	// Begin to assemble the justice kit, starting with the sweep address,
	// pubkeys, and csv delay.
	justiceKit := &blob.JusticeKit{
		BlobType:     blobType,
		SweepAddress: makeRandomP2PKHPkScript(),
		CSVDelay:     csvDelay,
	}
	copy(justiceKit.RevocationPubKey[:], revPK.SerializeCompressed())
	copy(justiceKit.LocalDelayPubKey[:], toLocalPK.SerializeCompressed())
	copy(justiceKit.CommitToRemotePubKey[:], toRemotePK.SerializeCompressed())

	// Create a transaction spending from the outputs of the breach
	// transaction created earlier. The inputs are always ordered w/
	// to-local and then to-remote. The outputs are always added as the
	// sweep address then reward address.
	justiceTxn := &wire.MsgTx{
		Version: 2,
		TxIn: []*wire.TxIn{
			{
				PreviousOutPoint: wire.OutPoint{
					Hash:  breachTxID,
					Index: 0,
				},
				ValueIn: breachTxn.TxOut[0].Value,
			},
			{
				PreviousOutPoint: wire.OutPoint{
					Hash:  breachTxID,
					Index: 1,
				},
				ValueIn:  breachTxn.TxOut[1].Value,
				Sequence: toRemoteSequence,
			},
		},
	}

	outputs, err := policy.ComputeJusticeTxOuts(
		totalAmount, txSize, justiceKit.SweepAddress,
		sessionInfo.RewardAddress,
	)
	require.Nil(t, err)

	// Attach the txouts and BIP69 sort the resulting transaction.
	justiceTxn.TxOut = outputs
	txsort.InPlaceSort(justiceTxn)

	// Create the sign descriptor used to sign for the to-local input.
	toLocalSignDesc := &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: revKeyLoc,
		},
		WitnessScript: toLocalScript,
		Output:        breachTxn.TxOut[0],
		InputIndex:    0,
		HashType:      txscript.SigHashAll,
	}

	// Create the sign descriptor used to sign for the to-remote input.
	toRemoteSignDesc := &input.SignDescriptor{
		KeyDesc: keychain.KeyDescriptor{
			KeyLocator: toRemoteKeyLoc,
			PubKey:     toRemotePK,
		},
		WitnessScript: toRemoteSigningScript,
		Output:        breachTxn.TxOut[1],
		InputIndex:    1,
		HashType:      txscript.SigHashAll,
	}

	// Verify that our test justice transaction is sane.
	err = standalone.CheckTransactionSanity(justiceTxn, uint64(netParams.MaxTxSize))
	require.Nil(t, err)

	// Compute a DER-encoded signature for the to-local input.
	toLocalSigRaw, err := signer.SignOutputRaw(justiceTxn, toLocalSignDesc)
	require.Nil(t, err)

	// Compute the witness for the to-remote input. The first element is a
	// DER-encoded signature under the to-remote pubkey. The sighash flag is
	// also present, so we trim it.
	toRemoteSigRaw, err := signer.SignOutputRaw(justiceTxn, toRemoteSignDesc)
	require.Nil(t, err)

	// Convert the DER to-local sig into a fixed-size signature.
	toLocalSig, err := lnwire.NewSigFromSignature(toLocalSigRaw)
	require.Nil(t, err)

	// Convert the DER to-remote sig into a fixed-size signature.
	toRemoteSig, err := lnwire.NewSigFromSignature(toRemoteSigRaw)
	require.Nil(t, err)

	// Complete our justice kit by copying the signatures into the payload.
	copy(justiceKit.CommitToLocalSig[:], toLocalSig[:])
	copy(justiceKit.CommitToRemoteSig[:], toRemoteSig[:])

	justiceDesc := &lookout.JusticeDescriptor{
		BreachedCommitTx: breachTxn,
		SessionInfo:      sessionInfo,
		JusticeKit:       justiceKit,
		NetParams:        netParams,
	}

	// Construct a breach punisher that will feed published transactions
	// over the buffered channel.
	publications := make(chan *wire.MsgTx, 1)
	punisher := lookout.NewBreachPunisher(&lookout.PunisherConfig{
		PublishTx: func(tx *wire.MsgTx, _ string) error {
			publications <- tx
			return nil
		},
	})

	// Exact retribution on the offender. If no error is returned, we expect
	// the justice transaction to be published via the channel.
	err = punisher.Punish(justiceDesc, nil)
	require.Nil(t, err)

	// Retrieve the published justice transaction.
	var wtJusticeTxn *wire.MsgTx
	select {
	case wtJusticeTxn = <-publications:
	case <-time.After(50 * time.Millisecond):
		t.Fatalf("punisher did not publish justice txn")
	}

	// Construct the test's to-local witness.
	wstack0 := make([][]byte, 3)
	wstack0[0] = append(toLocalSigRaw.Serialize(), byte(txscript.SigHashAll))
	wstack0[1] = []byte{1}
	wstack0[2] = toLocalScript
	justiceTxn.TxIn[0].SignatureScript, err = input.WitnessStackToSigScript(wstack0)
	if err != nil {
		t.Fatalf("error assembling wstack0: %v", err)
	}

	// Construct the test's to-remote witness.
	wstack1 := make([][]byte, 2)
	wstack1[0] = append(toRemoteSigRaw.Serialize(), byte(txscript.SigHashAll))
	wstack1[1] = toRemoteRedeemScript
	justiceTxn.TxIn[1].SignatureScript, err = input.WitnessStackToSigScript(wstack1)
	if err != nil {
		t.Fatalf("error assembling wstack1: %v", err)
	}

	// Assert that the watchtower derives the same justice txn.
	require.Equal(t, justiceTxn, wtJusticeTxn)
}
