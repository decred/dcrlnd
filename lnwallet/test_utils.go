package lnwallet

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"sync"

	"github.com/decred/dcrd/blockchain"
	"github.com/decred/dcrd/blockchain/stake"
	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/decred/dcrlnd/shachain"
)

var (
	// For simplicity a single priv key controls all of our test outputs.
	testWalletPrivKey = []byte{
		0x2b, 0xd8, 0x06, 0xc9, 0x7f, 0x0e, 0x00, 0xaf,
		0x1a, 0x1f, 0xc3, 0x32, 0x8f, 0xa7, 0x63, 0xa9,
		0x26, 0x97, 0x23, 0xc8, 0xdb, 0x8f, 0xac, 0x4f,
		0x93, 0xaf, 0x71, 0xdb, 0x18, 0x6d, 0x6e, 0x90,
	}

	// We're alice :)
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

	alicePkScript = []byte{
		0x76, 0xa9, 0x14, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xac}

	bobPkScript = []byte{
		0x76, 0xa9, 0x14, 0x11, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xac}

	// A serializable txn for testing funding txn.
	testTx = &wire.MsgTx{
		Version: 1,
		TxIn: []*wire.TxIn{
			{
				PreviousOutPoint: wire.OutPoint{
					Hash:  chainhash.Hash{},
					Index: 0xffffffff,
				},
				SignatureScript: []byte{0x04, 0x31, 0xdc, 0x00, 0x1b, 0x01, 0x62},
				Sequence:        0xffffffff,
			},
		},
		TxOut: []*wire.TxOut{
			{
				Value: 5000000000,
				PkScript: []byte{
					0x41, // OP_DATA_65
					0x04, 0xd6, 0x4b, 0xdf, 0xd0, 0x9e, 0xb1, 0xc5,
					0xfe, 0x29, 0x5a, 0xbd, 0xeb, 0x1d, 0xca, 0x42,
					0x81, 0xbe, 0x98, 0x8e, 0x2d, 0xa0, 0xb6, 0xc1,
					0xc6, 0xa5, 0x9d, 0xc2, 0x26, 0xc2, 0x86, 0x24,
					0xe1, 0x81, 0x75, 0xe8, 0x51, 0xc9, 0x6b, 0x97,
					0x3d, 0x81, 0xb0, 0x1c, 0xc3, 0x1f, 0x04, 0x78,
					0x34, 0xbc, 0x06, 0xd6, 0xd6, 0xed, 0xf6, 0x20,
					0xd1, 0x84, 0x24, 0x1a, 0x6a, 0xed, 0x8b, 0x63,
					0xa6, // 65-byte signature
					0xac, // OP_CHECKSIG
				},
			},
		},
		LockTime: 5,
	}
)

// CreateTestChannels creates to fully populated channels to be used within
// testing fixtures. The channels will be returned as if the funding process
// has just completed.  The channel itself is funded with 10 DCR, with 5 DCR
// allocated to each side. Within the channel, Alice is the initiator. The
// function also returns a "cleanup" function that is meant to be called once
// the test has been finalized. The clean up function will remote all temporary
// files created
func CreateTestChannels() (*LightningChannel, *LightningChannel, func(), error) {
	channelCapacity, err := dcrutil.NewAmount(10)
	if err != nil {
		return nil, nil, nil, err
	}

	channelBal := channelCapacity / 2
	aliceDustLimit := dcrutil.Amount(200)
	bobDustLimit := dcrutil.Amount(1300)
	csvTimeoutAlice := uint32(5)
	csvTimeoutBob := uint32(4)

	prevOut := &wire.OutPoint{
		Hash:  testHdSeed,
		Index: 0,
	}
	fundingTxIn := wire.NewTxIn(prevOut, 0, nil) // TODO(decred): Need correct input value

	// For each party, we'll create a distinct set of keys in order to
	// emulate the typical set up with live channels.
	var (
		aliceKeys []*secp256k1.PrivateKey
		bobKeys   []*secp256k1.PrivateKey
	)
	for i := 0; i < 5; i++ {
		key := make([]byte, len(testWalletPrivKey))
		copy(key, testWalletPrivKey)
		key[0] ^= byte(i + 1)

		aliceKey, _ := secp256k1.PrivKeyFromBytes(key)
		aliceKeys = append(aliceKeys, aliceKey)

		key = make([]byte, len(bobsPrivKey))
		copy(key, bobsPrivKey)
		key[0] ^= byte(i + 1)

		bobKey, _ := secp256k1.PrivKeyFromBytes(key)
		bobKeys = append(bobKeys, bobKey)
	}

	aliceCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit:        aliceDustLimit,
			MaxPendingAmount: lnwire.NewMAtomsFromAtoms(channelCapacity),
			ChanReserve:      channelCapacity / 100,
			MinHTLC:          0,
			MaxAcceptedHtlcs: MaxHTLCNumber / 2,
		},
		CsvDelay: uint16(csvTimeoutAlice),
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: aliceKeys[0].PubKey(),
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeys[1].PubKey(),
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeys[2].PubKey(),
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeys[3].PubKey(),
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: aliceKeys[4].PubKey(),
		},
	}
	bobCfg := channeldb.ChannelConfig{
		ChannelConstraints: channeldb.ChannelConstraints{
			DustLimit:        bobDustLimit,
			MaxPendingAmount: lnwire.NewMAtomsFromAtoms(channelCapacity),
			ChanReserve:      channelCapacity / 100,
			MinHTLC:          0,
			MaxAcceptedHtlcs: MaxHTLCNumber / 2,
		},
		CsvDelay: uint16(csvTimeoutBob),
		MultiSigKey: keychain.KeyDescriptor{
			PubKey: bobKeys[0].PubKey(),
		},
		RevocationBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeys[1].PubKey(),
		},
		PaymentBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeys[2].PubKey(),
		},
		DelayBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeys[3].PubKey(),
		},
		HtlcBasePoint: keychain.KeyDescriptor{
			PubKey: bobKeys[4].PubKey(),
		},
	}

	bobRoot, err := shachain.NewHash(bobKeys[0].Serialize())
	if err != nil {
		return nil, nil, nil, err
	}
	bobPreimageProducer := shachain.NewRevocationProducer(*bobRoot)
	bobFirstRevoke, err := bobPreimageProducer.AtIndex(0)
	if err != nil {
		return nil, nil, nil, err
	}
	bobCommitPoint := ComputeCommitmentPoint(bobFirstRevoke[:])

	aliceRoot, err := shachain.NewHash(aliceKeys[0].Serialize())
	if err != nil {
		return nil, nil, nil, err
	}
	alicePreimageProducer := shachain.NewRevocationProducer(*aliceRoot)
	aliceFirstRevoke, err := alicePreimageProducer.AtIndex(0)
	if err != nil {
		return nil, nil, nil, err
	}
	aliceCommitPoint := ComputeCommitmentPoint(aliceFirstRevoke[:])

	aliceCommitTx, bobCommitTx, err := CreateCommitmentTxns(channelBal,
		channelBal, &aliceCfg, &bobCfg, aliceCommitPoint, bobCommitPoint,
		*fundingTxIn, &chaincfg.RegNetParams)
	if err != nil {
		return nil, nil, nil, err
	}

	alicePath, err := ioutil.TempDir("", "alicedb")
	if err != nil {
		return nil, nil, nil, err
	}
	dbAlice, err := channeldb.Open(alicePath)
	if err != nil {
		return nil, nil, nil, err
	}

	bobPath, err := ioutil.TempDir("", "bobdb")
	if err != nil {
		return nil, nil, nil, err
	}
	dbBob, err := channeldb.Open(bobPath)
	if err != nil {
		return nil, nil, nil, err
	}

	// The rate for this estimator must be the same as what is returned by
	// calcStaticFee().
	estimator := NewStaticFeeEstimator(6000, 0)
	feePerKB, err := estimator.EstimateFeePerKB(1)
	if err != nil {
		return nil, nil, nil, err
	}
	commitFee := calcStaticFee(0)

	aliceCommit := channeldb.ChannelCommitment{
		CommitHeight:  0,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(channelBal - commitFee),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(channelBal),
		CommitFee:     commitFee,
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitTx:      aliceCommitTx,
		CommitSig:     bytes.Repeat([]byte{1}, 71),
	}
	bobCommit := channeldb.ChannelCommitment{
		CommitHeight:  0,
		LocalBalance:  lnwire.NewMAtomsFromAtoms(channelBal),
		RemoteBalance: lnwire.NewMAtomsFromAtoms(channelBal - commitFee),
		CommitFee:     commitFee,
		FeePerKB:      dcrutil.Amount(feePerKB),
		CommitTx:      bobCommitTx,
		CommitSig:     bytes.Repeat([]byte{1}, 71),
	}

	var chanIDBytes [8]byte
	if _, err := io.ReadFull(rand.Reader, chanIDBytes[:]); err != nil {
		return nil, nil, nil, err
	}

	shortChanID := lnwire.NewShortChanIDFromInt(
		binary.BigEndian.Uint64(chanIDBytes[:]),
	)

	aliceChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            aliceCfg,
		RemoteChanCfg:           bobCfg,
		IdentityPub:             aliceKeys[0].PubKey(),
		FundingOutpoint:         *prevOut,
		ShortChannelID:          shortChanID,
		ChanType:                channeldb.SingleFunder,
		IsInitiator:             true,
		Capacity:                channelCapacity,
		RemoteCurrentRevocation: bobCommitPoint,
		RevocationProducer:      alicePreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         aliceCommit,
		RemoteCommitment:        aliceCommit,
		Db:                      dbAlice,
		Packager:                channeldb.NewChannelPackager(shortChanID),
		FundingTxn:              testTx,
	}
	bobChannelState := &channeldb.OpenChannel{
		LocalChanCfg:            bobCfg,
		RemoteChanCfg:           aliceCfg,
		IdentityPub:             bobKeys[0].PubKey(),
		FundingOutpoint:         *prevOut,
		ShortChannelID:          shortChanID,
		ChanType:                channeldb.SingleFunder,
		IsInitiator:             false,
		Capacity:                channelCapacity,
		RemoteCurrentRevocation: aliceCommitPoint,
		RevocationProducer:      bobPreimageProducer,
		RevocationStore:         shachain.NewRevocationStore(),
		LocalCommitment:         bobCommit,
		RemoteCommitment:        bobCommit,
		Db:                      dbBob,
		Packager:                channeldb.NewChannelPackager(shortChanID),
	}

	aliceSigner := &mockSigner{privkeys: aliceKeys}
	bobSigner := &mockSigner{privkeys: bobKeys}

	pCache := &mockPreimageCache{
		// hash -> preimage
		preimageMap: make(map[[32]byte][]byte),
	}

	// TODO(roasbeef): make mock version of pre-image store

	alicePool := NewSigPool(1, aliceSigner)
	channelAlice, err := NewLightningChannel(
		aliceSigner, pCache, aliceChannelState, alicePool,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	alicePool.Start()

	bobPool := NewSigPool(1, bobSigner)
	channelBob, err := NewLightningChannel(
		bobSigner, pCache, bobChannelState, bobPool,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	bobPool.Start()

	err = SetStateNumHint(
		aliceCommitTx, 0, channelAlice.stateHintObfuscator,
	)
	if err != nil {
		return nil, nil, nil, err
	}
	err = SetStateNumHint(
		bobCommitTx, 0, channelAlice.stateHintObfuscator,
	)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := channelAlice.channelState.FullSync(); err != nil {
		return nil, nil, nil, err
	}
	if err := channelBob.channelState.FullSync(); err != nil {
		return nil, nil, nil, err
	}

	cleanUpFunc := func() {
		os.RemoveAll(bobPath)
		os.RemoveAll(alicePath)

		alicePool.Stop()
		bobPool.Stop()
	}

	// Now that the channel are open, simulate the start of a session by
	// having Alice and Bob extend their revocation windows to each other.
	err = initRevocationWindows(channelAlice, channelBob)
	if err != nil {
		return nil, nil, nil, err
	}

	return channelAlice, channelBob, cleanUpFunc, nil
}

// initRevocationWindows simulates a new channel being opened within the p2p
// network by populating the initial revocation windows of the passed
// commitment state machines.
func initRevocationWindows(chanA, chanB *LightningChannel) error {
	aliceNextRevoke, err := chanA.NextRevocationKey()
	if err != nil {
		return err
	}
	if err := chanB.InitNextRevocation(aliceNextRevoke); err != nil {
		return err
	}

	bobNextRevoke, err := chanB.NextRevocationKey()
	if err != nil {
		return err
	}
	if err := chanA.InitNextRevocation(bobNextRevoke); err != nil {
		return err
	}

	return nil
}

// mockSigner is a simple implementation of the Signer interface. Each one has
// a set of private keys in a slice and can sign messages using the appropriate
// one.
type mockSigner struct {
	privkeys  []*secp256k1.PrivateKey
	netParams *chaincfg.Params
}

func (m *mockSigner) SignOutputRaw(tx *wire.MsgTx, signDesc *SignDescriptor) ([]byte, error) {
	pubkey := signDesc.KeyDesc.PubKey
	switch {
	case signDesc.SingleTweak != nil:
		pubkey = TweakPubKeyWithTweak(pubkey, signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		pubkey = DeriveRevocationPubkey(pubkey, signDesc.DoubleTweak.PubKey())
	}

	hash160 := dcrutil.Hash160(pubkey.SerializeCompressed())
	privKey := m.findKey(hash160, signDesc.SingleTweak, signDesc.DoubleTweak)
	if privKey == nil {
		return nil, fmt.Errorf("mock signer does not have key")
	}

	sig, err := txscript.RawTxInSignature(tx, signDesc.InputIndex,
		signDesc.WitnessScript, txscript.SigHashAll, privKey)
	if err != nil {
		return nil, err
	}

	return sig[:len(sig)-1], nil
}

func (m *mockSigner) ComputeInputScript(tx *wire.MsgTx, signDesc *SignDescriptor) (*InputScript, error) {
	scriptType, addresses, _, err := txscript.ExtractPkScriptAddrs(
		signDesc.Output.Version, signDesc.Output.PkScript, m.netParams)
	if err != nil {
		return nil, err
	}

	switch scriptType {
	case txscript.PubKeyHashTy:
		privKey := m.findKey(addresses[0].ScriptAddress(), signDesc.SingleTweak,
			signDesc.DoubleTweak)
		if privKey == nil {
			return nil, fmt.Errorf("mock signer does not have key for "+
				"address %v", addresses[0])
		}

		scriptSig, err := txscript.SignatureScript(tx, signDesc.InputIndex,
			signDesc.Output.PkScript, txscript.SigHashAll, privKey, true)
		if err != nil {
			return nil, err
		}

		return &InputScript{ScriptSig: scriptSig}, nil

	default:
		return nil, fmt.Errorf("unexpected script type: %v", scriptType)
	}
}

// findKey searches through all stored private keys and returns one
// corresponding to the hashed pubkey if it can be found. The public key may
// either correspond directly to the private key or to the private key with a
// tweak applied.
func (m *mockSigner) findKey(needleHash160 []byte, singleTweak []byte,
	doubleTweak *secp256k1.PrivateKey) *secp256k1.PrivateKey {

	for _, privkey := range m.privkeys {
		// First check whether public key is directly derived from private key.
		hash160 := dcrutil.Hash160(privkey.PubKey().SerializeCompressed())
		if bytes.Equal(hash160, needleHash160) {
			return privkey
		}

		// Otherwise check if public key is derived from tweaked private key.
		switch {
		case singleTweak != nil:
			privkey = TweakPrivKey(privkey, singleTweak)
		case doubleTweak != nil:
			privkey = DeriveRevocationPrivKey(privkey, doubleTweak)
		default:
			continue
		}
		hash160 = dcrutil.Hash160(privkey.PubKey().SerializeCompressed())
		if bytes.Equal(hash160, needleHash160) {
			return privkey
		}
	}
	return nil
}

type mockPreimageCache struct {
	sync.Mutex
	preimageMap map[[32]byte][]byte
}

func (m *mockPreimageCache) LookupPreimage(hash []byte) ([]byte, bool) {
	m.Lock()
	defer m.Unlock()

	var h [32]byte
	copy(h[:], hash)

	p, ok := m.preimageMap[h]
	return p, ok
}

func (m *mockPreimageCache) AddPreimage(preimage []byte) error {
	m.Lock()
	defer m.Unlock()

	m.preimageMap[chainhash.HashH(preimage)] = preimage

	return nil
}

// pubkeyFromHex parses a Bitcoin public key from a hex encoded string.
func pubkeyFromHex(keyHex string) (*secp256k1.PublicKey, error) {
	bytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	return secp256k1.ParsePubKey(bytes)
}

// privkeyFromHex parses a Bitcoin private key from a hex encoded string.
func privkeyFromHex(keyHex string) (*secp256k1.PrivateKey, error) {
	bytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	key, _ := secp256k1.PrivKeyFromBytes(bytes)
	return key, nil

}

// pubkeyToHex serializes a Bitcoin public key to a hex encoded string.
func pubkeyToHex(key *secp256k1.PublicKey) string {
	return hex.EncodeToString(key.SerializeCompressed())
}

// privkeyFromHex serializes a Bitcoin private key to a hex encoded string.
func privkeyToHex(key *secp256k1.PrivateKey) string {
	return hex.EncodeToString(key.Serialize())
}

// signatureFromHex parses a Bitcoin signature from a hex encoded string.
func signatureFromHex(sigHex string) (*secp256k1.Signature, error) {
	bytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, err
	}
	return secp256k1.ParseSignature(bytes)
}

// blockFromHex parses a full Bitcoin block from a hex encoded string.
func blockFromHex(blockHex string) (*dcrutil.Block, error) {
	bytes, err := hex.DecodeString(blockHex)
	if err != nil {
		return nil, err
	}
	return dcrutil.NewBlockFromBytes(bytes)
}

// txFromHex parses a full Bitcoin transaction from a hex encoded string.
func txFromHex(txHex string) (*dcrutil.Tx, error) {
	bytes, err := hex.DecodeString(txHex)
	if err != nil {
		return nil, err
	}
	return dcrutil.NewTxFromBytes(bytes)
}

// calcStaticFee calculates appropriate fees for commitment transactions.  This
// function provides a simple way to allow test balance assertions to take fee
// calculations into account.
//
// This uses a fixed, hard-coded value of 6000 Atoms/KB as fee.
//
// TODO(bvu): Refactor when dynamic fee estimation is added.
func calcStaticFee(numHTLCs int) dcrutil.Amount {
	const (
		// TODO(decred) This was hardcoded here. Should I use static, hardcoded
		// values instead of estimateCommitmentTxSize?
		// commitWeight = dcrutil.Amount(724)
		// htlcWeight   = 172
		feePerKB = dcrutil.Amount(6000)
	)
	commitSize := EstimateCommitmentTxSize(numHTLCs)
	return feePerKB * dcrutil.Amount(commitSize) / 1000
}

// checkLnTransactionSanity checks whether an ln transaction (funding,
// commitment, etc) is reasonably sane according to consensus and standardness
// checks that don't require a full backing blockchain to verify.
func checkLnTransactionSanity(tx *wire.MsgTx, utxos map[wire.OutPoint]*wire.TxOut, netParams *chaincfg.Params) error {
	scriptFlagsForTest := txscript.ScriptDiscourageUpgradableNops |
		txscript.ScriptVerifyCleanStack |
		txscript.ScriptVerifyCheckLockTimeVerify |
		txscript.ScriptVerifyCheckSequenceVerify |
		txscript.ScriptVerifySHA256

	err := blockchain.CheckTransactionSanity(tx, netParams)
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
	if tx.Version != lnTxVersion {
		return fmt.Errorf("tx version (%d) different than expected (%d)",
			tx.Version, lnTxVersion)
	}
	for i, out := range tx.TxOut {
		if out.Version != txscript.DefaultScriptVersion {
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
			scriptFlagsForTest, utxo.Version, nil)
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
