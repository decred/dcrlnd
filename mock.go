package main

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
)

// The block height returned by the mock BlockChainIO's GetBestBlock.
const fundingBroadcastHeight = 123

type mockSigner struct {
	key *secp256k1.PrivateKey
}

func (m *mockSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *lnwallet.SignDescriptor) ([]byte, error) {
	witnessScript := signDesc.WitnessScript
	privKey := m.key

	if !privKey.PubKey().IsEqual(signDesc.KeyDesc.PubKey) {
		return nil, fmt.Errorf("incorrect key passed")
	}

	switch {
	case signDesc.SingleTweak != nil:
		privKey = lnwallet.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = lnwallet.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)
	}

	sig, err := txscript.RawTxInSignature(tx,
		signDesc.InputIndex, witnessScript, signDesc.HashType,
		privKey)
	if err != nil {
		return nil, err
	}

	return sig[:len(sig)-1], nil
}

func (m *mockSigner) ComputeInputScript(tx *wire.MsgTx,
	signDesc *lnwallet.SignDescriptor) (*lnwallet.InputScript, error) {

	// TODO(roasbeef): expose tweaked signer from lnwallet so don't need to
	// duplicate this code?

	privKey := m.key

	switch {
	case signDesc.SingleTweak != nil:
		privKey = lnwallet.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = lnwallet.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)
	}

	sigScript, err := txscript.SignatureScript(tx,
		signDesc.InputIndex, signDesc.Output.PkScript,
		signDesc.HashType, privKey, true)
	if err != nil {
		return nil, err
	}

	return &lnwallet.InputScript{
		ScriptSig: sigScript,
	}, nil
}

type mockNotfier struct {
	confChannel chan *chainntnfs.TxConfirmation
}

func (m *mockNotfier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
	_ []byte, numConfs, heightHint uint32) (*chainntnfs.ConfirmationEvent, error) {
	return &chainntnfs.ConfirmationEvent{
		Confirmed: m.confChannel,
	}, nil
}
func (m *mockNotfier) RegisterBlockEpochNtfn(
	bestBlock *chainntnfs.BlockEpoch) (*chainntnfs.BlockEpochEvent, error) {
	return &chainntnfs.BlockEpochEvent{
		Epochs: make(chan *chainntnfs.BlockEpoch),
		Cancel: func() {},
	}, nil
}

func (m *mockNotfier) Start() error {
	return nil
}

func (m *mockNotfier) Stop() error {
	return nil
}
func (m *mockNotfier) RegisterSpendNtfn(outpoint *wire.OutPoint, _ []byte,
	heightHint uint32) (*chainntnfs.SpendEvent, error) {
	return &chainntnfs.SpendEvent{
		Spend:  make(chan *chainntnfs.SpendDetail),
		Cancel: func() {},
	}, nil
}

// mockSpendNotifier extends the mockNotifier so that spend notifications can be
// triggered and delivered to subscribers.
type mockSpendNotifier struct {
	*mockNotfier
	spendMap map[wire.OutPoint][]chan *chainntnfs.SpendDetail
	mtx      sync.Mutex
}

func makeMockSpendNotifier() *mockSpendNotifier {
	return &mockSpendNotifier{
		mockNotfier: &mockNotfier{
			confChannel: make(chan *chainntnfs.TxConfirmation),
		},
		spendMap: make(map[wire.OutPoint][]chan *chainntnfs.SpendDetail),
	}
}

func (m *mockSpendNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	_ []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	spendChan := make(chan *chainntnfs.SpendDetail)
	m.spendMap[*outpoint] = append(m.spendMap[*outpoint], spendChan)
	return &chainntnfs.SpendEvent{
		Spend: spendChan,
		Cancel: func() {
		},
	}, nil
}

// Spend dispatches SpendDetails to all subscribers of the outpoint. The details
// will include the transaction and height provided by the caller.
func (m *mockSpendNotifier) Spend(outpoint *wire.OutPoint, height int32,
	txn *wire.MsgTx) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	if spendChans, ok := m.spendMap[*outpoint]; ok {
		delete(m.spendMap, *outpoint)
		for _, spendChan := range spendChans {
			txnHash := txn.TxHash()
			spendChan <- &chainntnfs.SpendDetail{
				SpentOutPoint:     outpoint,
				SpendingHeight:    height,
				SpendingTx:        txn,
				SpenderTxHash:     &txnHash,
				SpenderInputIndex: outpoint.Index,
			}
		}
	}
}

// hasPenderNotification checks whether the given outpoint has at least one
// client registered to receive spend notifications for the given outpoint.
func (m *mockSpendNotifier) hasSpenderNotification(outpoint *wire.OutPoint) bool {
	m.mtx.Lock()
	_, ok := m.spendMap[*outpoint]
	m.mtx.Unlock()
	return ok
}

type mockChainIO struct {
	bestHeight int32
}

func (m *mockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	return activeNetParams.GenesisHash, m.bestHeight, nil
}

func (*mockChainIO) GetUtxo(op *wire.OutPoint, _ []byte,
	heightHint uint32) (*wire.TxOut, error) {
	return nil, nil
}

func (*mockChainIO) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	return nil, nil
}

func (*mockChainIO) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return nil, nil
}

// mockWalletController is used by the LightningWallet, and let us mock the
// interaction with the Decred network.
type mockWalletController struct {
	rootKey               *secp256k1.PrivateKey
	prevAddres            dcrutil.Address
	publishedTransactions chan *wire.MsgTx
	index                 uint32
}

// BackEnd returns "mock" to signify a mock wallet controller.
func (*mockWalletController) BackEnd() string {
	return "mock"
}

// FetchInputInfo will be called to get info about the inputs to the funding
// transaction.
func (*mockWalletController) FetchInputInfo(
	prevOut *wire.OutPoint) (*wire.TxOut, error) {
	txOut := &wire.TxOut{
		Value:    int64(10 * dcrutil.AtomsPerCoin),
		PkScript: []byte("dummy"),
	}
	return txOut, nil
}
func (*mockWalletController) ConfirmedBalance(confs int32) (dcrutil.Amount, error) {
	return 0, nil
}

// NewAddress is called to get new addresses for delivery, change etc.
func (m *mockWalletController) NewAddress(addrType lnwallet.AddressType,
	change bool) (dcrutil.Address, error) {
	addr, _ := dcrutil.NewAddressSecpPubKeyCompressed(
		m.rootKey.PubKey(), &chaincfg.RegNetParams)
	return addr, nil
}

func (*mockWalletController) IsOurAddress(a dcrutil.Address) bool {
	return false
}

func (*mockWalletController) SendOutputs(outputs []*wire.TxOut,
	_ lnwallet.AtomPerKByte) (*wire.MsgTx, error) {

	return nil, nil
}

// ListUnspentWitness is called by the wallet when doing coin selection. We just
// need one unspent for the funding transaction.
func (m *mockWalletController) ListUnspentWitness(minconfirms,
	maxconfirms int32) ([]*lnwallet.Utxo, error) {
	utxo := &lnwallet.Utxo{
		AddressType: lnwallet.PubKeyHash,
		Value:       dcrutil.Amount(10 * dcrutil.AtomsPerCoin),
		PkScript:    make([]byte, 22),
		OutPoint: wire.OutPoint{
			Hash:  chainhash.Hash{},
			Index: m.index,
		},
	}
	atomic.AddUint32(&m.index, 1)
	var ret []*lnwallet.Utxo
	ret = append(ret, utxo)
	return ret, nil
}
func (*mockWalletController) ListTransactionDetails() ([]*lnwallet.TransactionDetail, error) {
	return nil, nil
}
func (*mockWalletController) LockOutpoint(o wire.OutPoint)   {}
func (*mockWalletController) UnlockOutpoint(o wire.OutPoint) {}
func (m *mockWalletController) PublishTransaction(tx *wire.MsgTx) error {
	m.publishedTransactions <- tx
	return nil
}
func (*mockWalletController) SubscribeTransactions() (lnwallet.TransactionSubscription, error) {
	return nil, nil
}
func (*mockWalletController) IsSynced() (bool, int64, error) {
	return true, int64(0), nil
}
func (*mockWalletController) Start() error {
	return nil
}
func (*mockWalletController) Stop() error {
	return nil
}

type mockSecretKeyRing struct {
	rootKey *secp256k1.PrivateKey
}

func (m *mockSecretKeyRing) DeriveNextKey(keyFam keychain.KeyFamily) (keychain.KeyDescriptor, error) {
	return keychain.KeyDescriptor{
		PubKey: m.rootKey.PubKey(),
	}, nil
}

func (m *mockSecretKeyRing) DeriveKey(keyLoc keychain.KeyLocator) (keychain.KeyDescriptor, error) {
	return keychain.KeyDescriptor{
		PubKey: m.rootKey.PubKey(),
	}, nil
}

func (m *mockSecretKeyRing) DerivePrivKey(keyDesc keychain.KeyDescriptor) (*secp256k1.PrivateKey, error) {
	return m.rootKey, nil
}

func (m *mockSecretKeyRing) ScalarMult(keyDesc keychain.KeyDescriptor,
	pubKey *secp256k1.PublicKey) ([]byte, error) {
	return nil, nil
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
