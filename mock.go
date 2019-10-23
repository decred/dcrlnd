package dcrlnd

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v2"
	"github.com/decred/dcrd/dcrec/secp256k1/v2"
	"github.com/decred/dcrd/dcrutil/v2"
	"github.com/decred/dcrd/txscript/v2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrwallet/wallet/v3/txauthor"

	"github.com/decred/dcrlnd/chainntnfs"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
	"github.com/decred/dcrlnd/lnwallet"
)

// The block height returned by the mock BlockChainIO's GetBestBlock.
const fundingBroadcastHeight = 123

type mockSigner struct {
	key *secp256k1.PrivateKey
}

func (m *mockSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) ([]byte, error) {
	witnessScript := signDesc.WitnessScript
	privKey := m.key

	if !privKey.PubKey().IsEqual(signDesc.KeyDesc.PubKey) {
		return nil, fmt.Errorf("incorrect key passed")
	}

	switch {
	case signDesc.SingleTweak != nil:
		privKey = input.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = input.DeriveRevocationPrivKey(privKey,
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
	signDesc *input.SignDescriptor) (*input.Script, error) {

	// TODO(roasbeef): expose tweaked signer from lnwallet so don't need to
	// duplicate this code?

	privKey := m.key

	switch {
	case signDesc.SingleTweak != nil:
		privKey = input.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = input.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)
	}

	sigScript, err := txscript.SignatureScript(tx,
		signDesc.InputIndex, signDesc.Output.PkScript,
		signDesc.HashType, privKey, true)
	if err != nil {
		return nil, err
	}

	return &input.Script{
		SigScript: sigScript,
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
	spends   map[wire.OutPoint]*chainntnfs.SpendDetail
	mtx      sync.Mutex
}

func makeMockSpendNotifier() *mockSpendNotifier {
	return &mockSpendNotifier{
		mockNotfier: &mockNotfier{
			confChannel: make(chan *chainntnfs.TxConfirmation),
		},
		spendMap: make(map[wire.OutPoint][]chan *chainntnfs.SpendDetail),
		spends:   make(map[wire.OutPoint]*chainntnfs.SpendDetail),
	}
}

func (m *mockSpendNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	_ []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	spendChan := make(chan *chainntnfs.SpendDetail, 1)
	if detail, ok := m.spends[*outpoint]; ok {
		// Deliver spend immediately if details are already known.
		spendChan <- &chainntnfs.SpendDetail{
			SpentOutPoint:     detail.SpentOutPoint,
			SpendingHeight:    detail.SpendingHeight,
			SpendingTx:        detail.SpendingTx,
			SpenderTxHash:     detail.SpenderTxHash,
			SpenderInputIndex: detail.SpenderInputIndex,
		}
	} else {
		// Otherwise, queue the notification for delivery if the spend
		// is ever received.
		m.spendMap[*outpoint] = append(m.spendMap[*outpoint], spendChan)
	}

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

	txnHash := txn.TxHash()
	details := &chainntnfs.SpendDetail{
		SpentOutPoint:     outpoint,
		SpendingHeight:    height,
		SpendingTx:        txn,
		SpenderTxHash:     &txnHash,
		SpenderInputIndex: outpoint.Index,
	}

	// Cache details in case of late registration.
	if _, ok := m.spends[*outpoint]; !ok {
		m.spends[*outpoint] = details
	}

	// Deliver any backlogged spend notifications.
	if spendChans, ok := m.spendMap[*outpoint]; ok {
		delete(m.spendMap, *outpoint)
		for _, spendChan := range spendChans {
			spendChan <- &chainntnfs.SpendDetail{
				SpentOutPoint:     details.SpentOutPoint,
				SpendingHeight:    details.SpendingHeight,
				SpendingTx:        details.SpendingTx,
				SpenderTxHash:     details.SpenderTxHash,
				SpenderInputIndex: details.SpenderInputIndex,
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

var _ lnwallet.BlockChainIO = (*mockChainIO)(nil)

func (m *mockChainIO) GetBestBlock() (*chainhash.Hash, int32, error) {
	return &activeNetParams.GenesisHash, m.bestHeight, nil
}

func (*mockChainIO) GetUtxo(op *wire.OutPoint, _ []byte,
	heightHint uint32, _ <-chan struct{}) (*wire.TxOut, error) {
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
	publishedTransactions chan *wire.MsgTx
	index                 uint32
	utxos                 []*lnwallet.Utxo
}

// BackEnd returns "mock" to signify a mock wallet controller.
func (*mockWalletController) BackEnd() string {
	return "mock"
}

// FetchInputInfo will be called to get info about the inputs to the funding
// transaction.
func (*mockWalletController) FetchInputInfo(
	prevOut *wire.OutPoint) (*lnwallet.Utxo, error) {
	utxo := &lnwallet.Utxo{
		AddressType:   lnwallet.PubKeyHash,
		Value:         10 * dcrutil.AtomsPerCoin,
		PkScript:      []byte("dummy"),
		Confirmations: 1,
		OutPoint:      *prevOut,
	}
	return utxo, nil
}
func (*mockWalletController) ConfirmedBalance(confs int32) (dcrutil.Amount, error) {
	return 0, nil
}

// NewAddress is called to get new addresses for delivery, change etc.
func (m *mockWalletController) NewAddress(addrType lnwallet.AddressType,
	change bool) (dcrutil.Address, error) {
	addr, _ := dcrutil.NewAddressSecpPubKeyCompressed(
		m.rootKey.PubKey(), chaincfg.RegNetParams())
	return addr, nil
}

func (*mockWalletController) LastUnusedAddress(addrType lnwallet.AddressType) (
	dcrutil.Address, error) {
	return nil, nil
}

func (*mockWalletController) IsOurAddress(a dcrutil.Address) bool {
	return false
}

func (*mockWalletController) SendOutputs(outputs []*wire.TxOut,
	_ lnwallet.AtomPerKByte) (*wire.MsgTx, error) {

	return nil, nil
}

func (*mockWalletController) CreateSimpleTx(outputs []*wire.TxOut,
	_ lnwallet.AtomPerKByte, _ bool) (*txauthor.AuthoredTx, error) {

	return nil, nil
}

// ListUnspentWitness is called by the wallet when doing coin selection. We just
// need one unspent for the funding transaction.
func (m *mockWalletController) ListUnspentWitness(minconfirms,
	maxconfirms int32) ([]*lnwallet.Utxo, error) {

	// If the mock already has a list of utxos, return it.
	if m.utxos != nil {
		return m.utxos, nil
	}

	// Otherwise create one to return.
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
func (*mockWalletController) InitialSyncChannel() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}
func (*mockWalletController) BestBlock() (int64, chainhash.Hash, int64, error) {
	return 0, chainhash.Hash{}, 0, nil
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
