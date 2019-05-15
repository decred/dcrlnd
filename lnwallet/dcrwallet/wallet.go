package dcrwallet

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil"
	"github.com/decred/dcrd/txscript"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/lnwallet"

	walletloader "github.com/decred/dcrwallet/loader"
	base "github.com/decred/dcrwallet/wallet/v2"
	"github.com/decred/dcrwallet/wallet/v2/txrules"
	"github.com/decred/dcrwallet/wallet/v2/udb"
)

const (
	defaultAccount = uint32(udb.DefaultAccountNum)
)

// DcrWallet is an implementation of the lnwallet.WalletController interface
// backed by an active instance of dcrwallet. At the time of the writing of
// this documentation, this implementation requires a full dcrd node to
// operate.
//
// This struct implements the lnwallet.Signer, lnWallet.MessageSigner,
// keychain.SecretKeyRing and keychain.KeyRing interfaces.
//
// Note that most of its functions might produce errors or panics until the
// wallet has been fully synced.
type DcrWallet struct {
	// wallet is an active instance of dcrwallet.
	wallet             *base.Wallet
	loader             *walletloader.Loader
	atomicWalletSynced uint32 // CAS (synced=1) when wallet syncing complete

	cfg *Config

	netParams *chaincfg.Params

	// utxoCache is a cache used to speed up repeated calls to
	// FetchInputInfo.
	utxoCache map[wire.OutPoint]*wire.TxOut
	cacheMtx  sync.RWMutex

	syncer WalletSyncer

	*walletKeyRing
}

// A compile time check to ensure that DcrWallet implements the
// WalletController interface.
var _ lnwallet.WalletController = (*DcrWallet)(nil)

// New returns a new fully initialized instance of DcrWallet given a valid
// configuration struct.
func New(cfg Config) (*DcrWallet, error) {

	wallet := cfg.Wallet
	loader := cfg.Loader
	syncer := cfg.Syncer

	if cfg.Syncer == nil {
		return nil, fmt.Errorf("cfg.Syncer needs to be specified")
	}

	if cfg.Wallet == nil {
		// Ensure the wallet exists or create it when the create flag
		// is specified
		netDir := NetworkDir(cfg.DataDir, cfg.NetParams)
		loader = walletloader.NewLoader(cfg.NetParams, netDir,
			&walletloader.StakeOptions{}, base.DefaultGapLimit, false,
			txrules.DefaultRelayFeePerKb.ToCoin(), base.DefaultAccountGapLimit,
			false)
		walletExists, err := loader.WalletExists()
		if err != nil {
			return nil, err
		}

		if !walletExists {
			// Wallet has never been created, perform initial set up.
			wallet, err = loader.CreateNewWallet(cfg.PublicPass, cfg.PrivatePass,
				cfg.HdSeed)
			if err != nil {
				return nil, err
			}
		} else {
			// Wallet has been created and been initialized at this point,
			// open it along with all the required DB namepsaces, and the
			// DB itself.
			wallet, err = loader.OpenExistingWallet(cfg.PublicPass)
			if err != nil {
				return nil, err
			}
		}
	}

	return &DcrWallet{
		cfg:       &cfg,
		wallet:    wallet,
		loader:    loader,
		syncer:    syncer,
		netParams: cfg.NetParams,
		utxoCache: make(map[wire.OutPoint]*wire.TxOut),
	}, nil
}

// BackEnd returns the underlying ChainService's name as a string.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) BackEnd() string {
	if _, is := b.syncer.(*RPCSyncer); is {
		// This package only supports full node backends for the moment
		return "dcrd"
	}

	return ""
}

// InternalWallet returns a pointer to the internal base wallet which is the
// core of dcrwallet.
func (b *DcrWallet) InternalWallet() *base.Wallet {
	return b.wallet
}

// Start initializes the underlying rpc connection, the wallet itself, and
// begins syncing to the current available blockchain state.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) Start() error {
	// We'll start by unlocking the wallet and ensuring that the KeyScope:
	// (1017, 1) exists within the internal waddrmgr. We'll need this in
	// order to properly generate the keys required for signing various
	// contracts.
	if err := b.wallet.Unlock(b.cfg.PrivatePass, nil); err != nil {
		return err
	}

	// And then start the syncer backend for this wallet.
	if err := b.syncer.start(b); err != nil {
		return err
	}

	return nil
}

// Stop signals the wallet for shutdown. Shutdown may entail closing
// any active sockets, database handles, stopping goroutines, etc.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) Stop() error {
	dcrwLog.Debug("Requesting wallet shutdown")
	b.wallet.Stop()
	b.syncer.stop()
	b.wallet.WaitForShutdown()

	dcrwLog.Debugf("Wallet has shut down")

	return nil
}

// ConfirmedBalance returns the sum of all the wallet's unspent outputs that
// have at least confs confirmations. If confs is set to zero, then all unspent
// outputs, including those currently in the mempool will be included in the
// final sum.
//
// This is a part of the WalletController interface.
//
// TODO(matheusd) Remove witness argument, given that's not applicable to decred
func (b *DcrWallet) ConfirmedBalance(confs int32) (dcrutil.Amount, error) {

	balances, err := b.wallet.CalculateAccountBalance(defaultAccount, confs)
	if err != nil {
		return 0, err
	}

	return balances.Spendable, nil
}

// NewAddress returns the next external or internal address for the wallet
// dictated by the value of the `change` parameter. If change is true, then an
// internal address will be returned, otherwise an external address should be
// returned.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) NewAddress(t lnwallet.AddressType, change bool) (dcrutil.Address, error) {

	switch t {
	case lnwallet.PubKeyHash:
		// nop
	// lnwallet.{WitnessPubKey,PubKeyHash} are both as p2pkh in decred
	case lnwallet.WitnessPubKey:
		// TODO(decred): this should fail so we can remove lnwallet.WitnessPubKey
		panic("remove witnessPubKey address requests")
	case lnwallet.NestedWitnessPubKey:
		// TODO(matheusd) this is likely to be changed to a p2sh
		panic("remove nestedWitnessPubKey address requests")
	default:
		return nil, fmt.Errorf("unknown address type")
	}

	if change {
		return b.wallet.NewInternalAddress(defaultAccount)
	}

	return b.wallet.NewExternalAddress(defaultAccount)
}

// IsOurAddress checks if the passed address belongs to this wallet
//
// This is a part of the WalletController interface.
func (b *DcrWallet) IsOurAddress(a dcrutil.Address) bool {
	result, err := b.wallet.HaveAddress(a)
	return result && (err == nil)
}

// SendOutputs funds, signs, and broadcasts a Decred transaction paying out to
// the specified outputs. In the case the wallet has insufficient funds, or the
// outputs are non-standard, a non-nil error will be returned.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) SendOutputs(outputs []*wire.TxOut,
	feeRate lnwallet.AtomPerKByte) (*wire.MsgTx, error) {

	// Ensure we haven't changed the default relay fee.
	// TODO(decred) Potentially change to a construct/sign/publish cycle or
	// add the fee as a parameter so that we don't risk changing the default
	// fee rate.
	oldRelayFee := b.wallet.RelayFee()
	b.wallet.SetRelayFee(dcrutil.Amount(feeRate))
	defer b.wallet.SetRelayFee(oldRelayFee)

	txHash, err := b.wallet.SendOutputs(outputs, defaultAccount, 1)
	if err != nil {
		return nil, err
	}

	txs, _, err := b.wallet.GetTransactionsByHashes([]*chainhash.Hash{txHash})
	if err != nil {
		return nil, err
	}

	return txs[0], nil
}

// LockOutpoint marks an outpoint as locked meaning it will no longer be deemed
// as eligible for coin selection. Locking outputs are utilized in order to
// avoid race conditions when selecting inputs for usage when funding a
// channel.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) LockOutpoint(o wire.OutPoint) {
	b.wallet.LockOutpoint(o)
}

// UnlockOutpoint unlocks a previously locked output, marking it eligible for
// coin selection.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) UnlockOutpoint(o wire.OutPoint) {
	b.wallet.UnlockOutpoint(o)
}

// ListUnspentWitness returns a slice of all the unspent outputs the wallet
// controls which pay to witness programs either directly or indirectly.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ListUnspentWitness(minConfs, maxConfs int32) (
	[]*lnwallet.Utxo, error) {
	// First, grab all the unfiltered currently unspent outputs.
	unspentOutputs, err := b.wallet.ListUnspent(minConfs, maxConfs, nil)
	if err != nil {
		return nil, err
	}

	// Convert the dcrjson formatted unspents into lnwallet.Utxo's
	witnessOutputs := make([]*lnwallet.Utxo, 0, len(unspentOutputs))
	for _, output := range unspentOutputs {
		pkScript, err := hex.DecodeString(output.ScriptPubKey)
		if err != nil {
			return nil, err
		}

		scriptClass := txscript.GetScriptClass(txscript.DefaultScriptVersion, pkScript)
		if scriptClass != txscript.PubKeyHashTy {
			continue
		}

		addressType := lnwallet.PubKeyHash
		txid, err := chainhash.NewHashFromStr(output.TxID)
		if err != nil {
			return nil, err
		}

		// We'll ensure we properly convert the amount given in
		// DCR to atoms.
		amt, err := dcrutil.NewAmount(output.Amount)
		if err != nil {
			return nil, err
		}

		utxo := &lnwallet.Utxo{
			AddressType: addressType,
			Value:       amt,
			PkScript:    pkScript,
			OutPoint: wire.OutPoint{
				Hash:  *txid,
				Index: output.Vout,
				Tree:  output.Tree,
			},
			Confirmations: output.Confirmations,
		}
		witnessOutputs = append(witnessOutputs, utxo)
	}

	return witnessOutputs, nil
}

// PublishTransaction performs cursory validation (dust checks, etc), then
// finally broadcasts the passed transaction to the Decred network. If
// publishing the transaction fails, an error describing the reason is
// returned (currently ErrDoubleSpend). If the transaction is already
// published to the network (either in the mempool or chain) no error
// will be returned.
func (b *DcrWallet) PublishTransaction(tx *wire.MsgTx) error {
	serTx, err := tx.Bytes()
	if err != nil {
		return err
	}

	n, err := b.wallet.NetworkBackend()
	if err != nil {
		return err
	}
	if n == nil {
		return fmt.Errorf("wallet does not have an active backend")
	}
	_, err = b.wallet.PublishTransaction(tx, serTx, n)
	if err != nil {
		// TODO(decred): review if the string messages are correct. Possible
		// convert from checking the message to checking the op.
		if strings.Contains(err.Error(), "already have") {
			// Transaction was already in the mempool, do
			// not treat as an error. We do this to mimic
			// the behaviour of bitcoind, which will not
			// return an error if a transaction in the
			// mempool is sent again using the
			// sendrawtransaction RPC call.
			return nil
		}
		if strings.Contains(err.Error(), "already exists") {
			// Transaction was already mined, we don't
			// consider this an error.
			return nil
		}
		if strings.Contains(err.Error(), "by double spending") {
			// Output was already spent.
			return lnwallet.ErrDoubleSpend
		}
		if strings.Contains(err.Error(), "already spent") {
			// Output was already spent.
			return lnwallet.ErrDoubleSpend
		}
		if strings.Contains(err.Error(), "already been spent") {
			// Output was already spent.
			return lnwallet.ErrDoubleSpend
		}
		if strings.Contains(err.Error(), "orphan transaction") {
			// Transaction is spending either output that
			// is missing or already spent.
			return lnwallet.ErrDoubleSpend
		}
		if strings.Contains(err.Error(), "by double spending") {
			// Wallet has a conflicting unmined transaction.
			return lnwallet.ErrDoubleSpend
		}
		return err
	}
	return nil
}

// extractBalanceDelta extracts the net balance delta from the PoV of the
// wallet given a TransactionSummary.
func extractBalanceDelta(
	txSummary base.TransactionSummary,
	tx *wire.MsgTx,
) (dcrutil.Amount, error) {
	// For each input we debit the wallet's outflow for this transaction,
	// and for each output we credit the wallet's inflow for this
	// transaction.
	var balanceDelta dcrutil.Amount
	for _, input := range txSummary.MyInputs {
		balanceDelta -= input.PreviousAmount
	}
	for _, output := range txSummary.MyOutputs {
		balanceDelta += dcrutil.Amount(tx.TxOut[output.Index].Value)
	}

	return balanceDelta, nil
}

// minedTransactionsToDetails is a helper function which converts a summary
// information about mined transactions to a TransactionDetail.
func minedTransactionsToDetails(
	currentHeight int32,
	block *base.Block,
	chainParams *chaincfg.Params,
) ([]*lnwallet.TransactionDetail, error) {

	if block.Header == nil {
		return nil, fmt.Errorf("cannot use minedTransactionsToDetails with mempoool")
	}

	details := make([]*lnwallet.TransactionDetail, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		wireTx := &wire.MsgTx{}
		txReader := bytes.NewReader(tx.Transaction)

		if err := wireTx.Deserialize(txReader); err != nil {
			return nil, err
		}

		var destAddresses []dcrutil.Address
		for _, txOut := range wireTx.TxOut {
			_, outAddresses, _, err :=
				txscript.ExtractPkScriptAddrs(txOut.Version, txOut.PkScript,
					chainParams)
			if err != nil {
				return nil, err
			}

			destAddresses = append(destAddresses, outAddresses...)
		}

		blockHash := block.Header.BlockHash()
		txDetail := &lnwallet.TransactionDetail{
			Hash:             *tx.Hash,
			NumConfirmations: currentHeight - int32(block.Header.Height) + 1,
			BlockHash:        &blockHash,
			BlockHeight:      int32(block.Header.Height),
			Timestamp:        block.Header.Timestamp.Unix(),
			TotalFees:        int64(tx.Fee),
			DestAddresses:    destAddresses,
		}

		balanceDelta, err := extractBalanceDelta(tx, wireTx)
		if err != nil {
			return nil, err
		}
		txDetail.Value = balanceDelta

		details = append(details, txDetail)
	}

	return details, nil
}

// unminedTransactionsToDetail is a helper function which converts a summary
// for an unconfirmed transaction to a transaction detail.
func unminedTransactionsToDetail(
	summary base.TransactionSummary,
) (*lnwallet.TransactionDetail, error) {
	wireTx := &wire.MsgTx{}
	txReader := bytes.NewReader(summary.Transaction)

	if err := wireTx.Deserialize(txReader); err != nil {
		return nil, err
	}

	txDetail := &lnwallet.TransactionDetail{
		Hash:      *summary.Hash,
		TotalFees: int64(summary.Fee),
		Timestamp: summary.Timestamp,
	}

	balanceDelta, err := extractBalanceDelta(summary, wireTx)
	if err != nil {
		return nil, err
	}
	txDetail.Value = balanceDelta

	return txDetail, nil
}

// ListTransactionDetails returns a list of all transactions which are
// relevant to the wallet.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ListTransactionDetails() ([]*lnwallet.TransactionDetail, error) {
	// Grab the best block the wallet knows of, we'll use this to calculate
	// # of confirmations shortly below.
	_, currentHeight := b.wallet.MainChainTip()

	// Iterating over transactions using the range 0..-1 goes through all mined
	// transactions (in ascending order) then through unmined transactions.
	start := base.NewBlockIdentifierFromHeight(0)
	stop := base.NewBlockIdentifierFromHeight(-1)

	txDetails := make([]*lnwallet.TransactionDetail, 0)

	rangeFn := func(block *base.Block) (bool, error) {
		isMined := block.Header != nil
		for _, tx := range block.Transactions {
			if isMined {
				details, err := minedTransactionsToDetails(currentHeight, block,
					b.netParams)
				if err != nil {
					return true, err
				}

				txDetails = append(txDetails, details...)
			} else {
				detail, err := unminedTransactionsToDetail(tx)
				if err != nil {
					return true, err
				}

				txDetails = append(txDetails, detail)
			}
		}

		return false, nil
	}

	err := b.wallet.GetTransactions(rangeFn, start, stop)
	if err != nil {
		return nil, err
	}

	return txDetails, nil
}

// txSubscriptionClient encapsulates the transaction notification client from
// the base wallet. Notifications received from the client will be proxied over
// two distinct channels.
type txSubscriptionClient struct {
	txClient base.TransactionNotificationsClient

	confirmed   chan *lnwallet.TransactionDetail
	unconfirmed chan *lnwallet.TransactionDetail

	w *base.Wallet

	wg   sync.WaitGroup
	quit chan struct{}
}

// ConfirmedTransactions returns a channel which will be sent on as new
// relevant transactions are confirmed.
//
// This is part of the TransactionSubscription interface.
func (t *txSubscriptionClient) ConfirmedTransactions() chan *lnwallet.TransactionDetail {
	return t.confirmed
}

// UnconfirmedTransactions returns a channel which will be sent on as
// new relevant transactions are seen within the network.
//
// This is part of the TransactionSubscription interface.
func (t *txSubscriptionClient) UnconfirmedTransactions() chan *lnwallet.TransactionDetail {
	return t.unconfirmed
}

// Cancel finalizes the subscription, cleaning up any resources allocated.
//
// This is part of the TransactionSubscription interface.
func (t *txSubscriptionClient) Cancel() {
	close(t.quit)
	t.wg.Wait()

	t.txClient.Done()
}

// notificationProxier proxies the notifications received by the underlying
// wallet's notification client to a higher-level TransactionSubscription
// client.
func (t *txSubscriptionClient) notificationProxier() {
out:
	for {
		select {
		case txNtfn := <-t.txClient.C:
			// TODO(roasbeef): handle detached blocks
			_, currentHeight := t.w.MainChainTip()

			// Launch a goroutine to re-package and send
			// notifications for any newly confirmed transactions.
			go func() {
				for _, block := range txNtfn.AttachedBlocks {
					details, err := minedTransactionsToDetails(currentHeight, &block, t.w.ChainParams())
					if err != nil {
						continue
					}

					for _, d := range details {
						select {
						case t.confirmed <- d:
						case <-t.quit:
							return
						}
					}
				}

			}()

			// Launch a goroutine to re-package and send
			// notifications for any newly unconfirmed transactions.
			go func() {
				for _, tx := range txNtfn.UnminedTransactions {
					detail, err := unminedTransactionsToDetail(tx)
					if err != nil {
						continue
					}

					select {
					case t.unconfirmed <- detail:
					case <-t.quit:
						return
					}
				}
			}()
		case <-t.quit:
			break out
		}
	}

	t.wg.Done()
}

// SubscribeTransactions returns a TransactionSubscription client which
// is capable of receiving async notifications as new transactions
// related to the wallet are seen within the network, or found in
// blocks.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) SubscribeTransactions() (lnwallet.TransactionSubscription, error) {
	walletClient := b.wallet.NtfnServer.TransactionNotifications()

	txClient := &txSubscriptionClient{
		txClient:    walletClient,
		confirmed:   make(chan *lnwallet.TransactionDetail),
		unconfirmed: make(chan *lnwallet.TransactionDetail),
		w:           b.wallet,
		quit:        make(chan struct{}),
	}
	txClient.wg.Add(1)
	go txClient.notificationProxier()

	return txClient, nil
}

// IsSynced returns a boolean indicating if from the PoV of the wallet,
// it has fully synced to the current best block in the main chain.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) IsSynced() (bool, int64, error) {
	// Grab the best chain state the wallet is currently aware of.
	walletBestHash, walletBestHeight := b.wallet.MainChainTip()
	walletBestHeader, err := b.wallet.BlockHeader(&walletBestHash)
	if err != nil {
		return false, 0, err
	}

	// Next, query the chain backend to grab the info about the tip of the
	// main chain.
	_, bestHeight, err := b.syncer.GetBestBlock()
	if err != nil {
		return false, 0, err
	}

	// If the wallet hasn't yet fully synced to the node's best chain tip,
	// then we're not yet fully synced.
	if walletBestHeight < bestHeight {
		return false, walletBestHeader.Timestamp.Unix(), nil
	}

	// If the timestamp on the best header is more than 2 hours in the
	// past, then we're not yet synced.
	minus2Hours := time.Now().Add(-2 * time.Hour)
	if walletBestHeader.Timestamp.Before(minus2Hours) {
		return false, walletBestHeader.Timestamp.Unix(), nil
	}

	// Check if the wallet has completed the initial sync procedure (discover
	// addresses, load tx filter, etc).
	walletSynced := atomic.LoadUint32(&b.atomicWalletSynced) == 1

	return walletSynced, walletBestHeader.Timestamp.Unix(), nil
}

func (b *DcrWallet) onRPCSyncerSynced(synced bool) {
	dcrwLog.Debug("RPC syncer notified wallet is synced")

	// Now that the wallet is synced and address discovery has ended, we
	// can create the keyring. We can only do this here (after sync)
	// because address discovery might upgrade the underlying dcrwallet
	// coin type.
	var err error
	b.walletKeyRing, err = newWalletKeyRing(b.wallet, b.cfg.DB)
	if err != nil {
		// Sign operations will fail, so signal the error and prevent
		// the wallet from considering itself synced (to prevent usage)
		dcrwLog.Errorf("Unable to create wallet key ring: %v", err)
		return
	}

	atomic.StoreUint32(&b.atomicWalletSynced, 1)
}
