package mock

import (
	"encoding/hex"
	"sync/atomic"
	"time"

	"decred.org/dcrwallet/v3/wallet"
	"decred.org/dcrwallet/v3/wallet/txauthor"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/internal/psbt"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

var (
	CoinPkScript, _ = hex.DecodeString("76a914000000000000000000000000000000000000000088ac")
)

// WalletController is a mock implementation of the WalletController
// interface. It let's us mock the interaction with the bitcoin network.
type WalletController struct {
	RootKey               *secp256k1.PrivateKey
	PublishedTransactions chan *wire.MsgTx
	index                 uint32
	Utxos                 []*lnwallet.Utxo
}

// BackEnd returns "mock" to signify a mock wallet controller.
func (w *WalletController) BackEnd() string {
	return "mock"
}

// FetchInputInfo will be called to get info about the inputs to the funding
// transaction.
func (w *WalletController) FetchInputInfo(
	prevOut *wire.OutPoint) (*lnwallet.Utxo, error) {

	utxo := &lnwallet.Utxo{
		AddressType:   lnwallet.PubKeyHash,
		Value:         15 * dcrutil.AtomsPerCoin,
		PkScript:      CoinPkScript,
		Confirmations: 1,
		OutPoint:      *prevOut,
	}
	return utxo, nil
}

// ConfirmedBalance currently returns dummy values.
func (w *WalletController) ConfirmedBalance(confs int32, account string) (dcrutil.Amount, error) {
	return 0, nil
}

// NewAddress is called to get new addresses for delivery, change etc.
func (w *WalletController) NewAddress(addrType lnwallet.AddressType,
	change bool, account string) (stdaddr.Address, error) {

	addr, _ := stdaddr.NewAddressPubKeyEcdsaSecp256k1V0(
		w.RootKey.PubKey(), chaincfg.RegNetParams(),
	)
	return addr, nil
}

// LastUnusedAddress currently returns dummy values.
func (w *WalletController) LastUnusedAddress(addrType lnwallet.AddressType,
	account string) (
	stdaddr.Address, error) {
	return nil, nil
}

// IsOurAddress currently returns a dummy value.
func (w *WalletController) IsOurAddress(a stdaddr.Address) bool {
	return false
}

// ListAccountscurrently returns a dummy value.
func (w *WalletController) ListAccounts(string) ([]wallet.AccountProperties, error) {
	return nil, nil
}

// ImportAccountcurrently returns a dummy value.
func (w *WalletController) ImportAccount(name string, accountPubKey *hdkeychain.ExtendedKey) error {
	return nil
}

// ImportPublicKeycurrently returns a dummy value.
func (w *WalletController) ImportPublicKey(pubKey *secp256k1.PublicKey) error {
	return nil
}

// SendOutputs currently returns dummy values.
func (w *WalletController) SendOutputs(outputs []*wire.TxOut,
	_ chainfee.AtomPerKByte, _ int32, _, _ string) (*wire.MsgTx, error) {

	return nil, nil
}

// CreateSimpleTx currently returns dummy values.
func (w *WalletController) CreateSimpleTx(outputs []*wire.TxOut,
	_ chainfee.AtomPerKByte, _ int32, _ bool) (*txauthor.AuthoredTx, error) {

	return nil, nil
}

// ListUnspentWitness is called by the wallet when doing coin selection. We just
// need one unspent for the funding transaction.
func (w *WalletController) ListUnspentWitness(minConfs,
	maxConfs int32, _ string) ([]*lnwallet.Utxo, error) {

	// If the mock already has a list of utxos, return it.
	if w.Utxos != nil {
		return w.Utxos, nil
	}

	// Otherwise create one to return.
	utxo := &lnwallet.Utxo{
		AddressType: lnwallet.PubKeyHash,
		Value:       dcrutil.Amount(15 * dcrutil.AtomsPerCoin),
		PkScript:    CoinPkScript,
		OutPoint: wire.OutPoint{
			Hash:  chainhash.Hash{},
			Index: w.index,
		},
	}
	atomic.AddUint32(&w.index, 1)
	var ret []*lnwallet.Utxo
	ret = append(ret, utxo)
	return ret, nil
}

// ListTransactionDetails currently returns dummy values.
func (w *WalletController) ListTransactionDetails(_,
	_ int32, _ string) ([]*lnwallet.TransactionDetail, error) {

	return nil, nil
}

// LockOutpoint currently does nothing.
func (w *WalletController) LockOutpoint(o wire.OutPoint) {}

// UnlockOutpoint currently does nothing.
func (w *WalletController) UnlockOutpoint(o wire.OutPoint) {}

// LeaseOutput returns the current time and a nil error.
func (w *WalletController) LeaseOutput(lnwallet.LockID, wire.OutPoint,
	time.Duration) (time.Time, error) {
	return time.Now(), nil
}

// ReleaseOutput currently does nothing.
func (w *WalletController) ReleaseOutput(lnwallet.LockID, wire.OutPoint) error {
	return nil
}

func (w *WalletController) ListLeasedOutputs() ([]*lnwallet.LockedOutput, error) {
	return nil, nil
}

// FundPsbt currently does nothing.
func (w *WalletController) FundPsbt(_ *psbt.Packet,
	_ chainfee.AtomPerKByte) (int32, error) {

	return 0, nil
}

// FinalizePsbt currently does nothing.
func (w *WalletController) FinalizePsbt(_ *psbt.Packet) error {
	return nil
}

// PublishTransaction sends a transaction to the PublishedTransactions chan.
func (w *WalletController) PublishTransaction(tx *wire.MsgTx, _ string) error {
	w.PublishedTransactions <- tx
	return nil
}

// LabelTransaction currently does nothing.
func (w *WalletController) LabelTransaction(_ chainhash.Hash, _ string,
	_ bool) error {

	return nil
}

// SubscribeTransactions currently does nothing.
func (w *WalletController) SubscribeTransactions() (lnwallet.TransactionSubscription,
	error) {

	return nil, nil
}

// AbandonDoubleSpends currently returns dummy values.
func (w *WalletController) AbandonDoubleSpends(spentOutpoints ...*wire.OutPoint) error {
	return nil
}

// InitialSyncChannelcurrently returns dummy values.
func (w *WalletController) InitialSyncChannel() <-chan struct{} {
	return nil
}

// BestBlock currently returns dummy values.
func (w *WalletController) BestBlock() (int64, chainhash.Hash, int64, error) {
	return 0, chainhash.Hash{}, 0, nil
}

// IsSynced currently returns dummy values.
func (w *WalletController) IsSynced() (bool, int64, error) {
	return true, int64(0), nil
}

// GetRecoveryInfo currently returns dummy values.
func (w *WalletController) GetRecoveryInfo() (bool, float64, error) {
	return true, float64(1), nil
}

// Start currently does nothing.
func (w *WalletController) Start() error {
	return nil
}

// Stop currently does nothing.
func (w *WalletController) Stop() error {
	return nil
}
