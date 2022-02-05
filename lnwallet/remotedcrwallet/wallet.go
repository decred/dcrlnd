package remotedcrwallet

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "decred.org/dcrwallet/v2/rpc/walletrpc"
	"google.golang.org/grpc"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/hdkeychain/v3"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/chainfee"
)

type DcrWallet struct {
	// syncedChan is a channel that is closed once the wallet has initially synced
	// to the network. It is protected by atomicWalletSynced.
	syncedChan chan struct{}

	// atomicWalletSynced is an atomic CAS flag (synced = 1) that indicates
	// when syncing has completed.
	atomicWalletSynced uint32

	// conn is the underlying grpc socket connection.
	conn *grpc.ClientConn

	cfg Config

	chainParams *chaincfg.Params
	db          *channeldb.DB

	*remoteWalletKeyRing

	branchExtXPriv *hdkeychain.ExtendedKey
	branchIntXPriv *hdkeychain.ExtendedKey

	// account is the account number which controls onchain funds available
	// for use from dcrlnd.
	account uint32

	// lockedOutpointsMu controls access to lockedOutpoints.
	lockedOutpointsMu sync.Mutex

	// lockedOutpoints is a list of outputs that the wallet won't use for
	// other operations. This is necessary in the driver because grpc
	// doesn't provide an entpoint to control this directly in the wallet.
	lockedOutpoints map[wire.OutPoint]struct{}

	wallet    pb.WalletServiceClient
	network   pb.NetworkServiceClient
	ctx       context.Context
	cancelCtx func()
}

// A compile time check to ensure that DcrWallet implements the
// WalletController interface.
var _ lnwallet.WalletController = (*DcrWallet)(nil)

// Compile time check to ensure that Dcrwallet implements the
// onchainAddrSourcer interface.
var _ (onchainAddrSourcer) = (*DcrWallet)(nil)

func New(cfg Config) (*DcrWallet, error) {

	if cfg.Conn == nil {
		return nil, fmt.Errorf("conn cannot be empty")
	}

	// TODO(decred): Check if the node is synced before allowing this to
	// proceed.
	ctxb := context.Background()
	wallet := pb.NewWalletServiceClient(cfg.Conn)

	// Unlock the account.
	unlockAcctReq := &pb.UnlockAccountRequest{
		AccountNumber: uint32(cfg.AccountNumber),
		Passphrase:    cfg.PrivatePass,
	}
	_, err := wallet.UnlockAccount(ctxb, unlockAcctReq)
	if err != nil {
		return nil, fmt.Errorf("unable to unlock account: %v", err)
	}

	// Obtain the root master priv key from which all LN-related
	// keys are derived. By convention, this is a special branch in the
	// passed account.
	network := pb.NewNetworkServiceClient(cfg.Conn)
	req := &pb.GetAccountExtendedPrivKeyRequest{
		AccountNumber: uint32(cfg.AccountNumber),
	}
	resp, err := wallet.GetAccountExtendedPrivKey(ctxb, req)

	// Irrespective of the return of GetAccountExtendedPrivKey, re-lock the
	// account.
	lockAcctReq := &pb.LockAccountRequest{
		AccountNumber: uint32(cfg.AccountNumber),
	}
	_, lockErr := wallet.LockAccount(ctxb, lockAcctReq)
	if lockErr != nil {
		dcrwLog.Errorf("Error while locking account number %d: %v",
			cfg.AccountNumber, lockErr)
	}

	// And now check if GetAccountExtendedPrivKey returned an error.
	if err != nil {
		return nil, fmt.Errorf("unable to get master LN account "+
			"extended priv key: %v", err)
	}

	acctXPriv, err := hdkeychain.NewKeyFromString(
		resp.AccExtendedPrivKey, cfg.NetParams,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to create account xpriv: %v", err)
	}

	// Derive and store the account's external and internal extended priv
	// keys so that we can redeem funds stored in this account's utxos and
	// use them to fund channels, send coins to other nodes, etc.
	branchExtXPriv, err := acctXPriv.Child(0)
	if err != nil {
		return nil, fmt.Errorf("unable to derive the external branch xpriv: %v", err)
	}
	branchIntXPriv, err := acctXPriv.Child(1)
	if err != nil {
		return nil, fmt.Errorf("unable to derive the internal branch xpriv: %v", err)
	}

	// Ensure we don't attempt to use a keyring derived from a different
	// account than previously used by comparing the first external public
	// key with the one stored in the database.
	firstKey, err := branchExtXPriv.Child(0)
	if err != nil {
		return nil, fmt.Errorf("unable to derive first external key: %v", err)
	}
	firstPubKeyBytes := firstKey.SerializedPubKey()
	if err = cfg.DB.CompareAndStoreAccountID(firstPubKeyBytes); err != nil {
		return nil, fmt.Errorf("account number %d failed to generate "+
			"previously stored account ID: %v", cfg.AccountNumber, err)
	}

	ctx, cancelCtx := context.WithCancel(ctxb)
	dcrw := &DcrWallet{
		account:         uint32(cfg.AccountNumber),
		syncedChan:      make(chan struct{}),
		chainParams:     cfg.NetParams,
		db:              cfg.DB,
		cfg:             cfg,
		conn:            cfg.Conn,
		wallet:          wallet,
		network:         network,
		branchExtXPriv:  branchExtXPriv,
		branchIntXPriv:  branchIntXPriv,
		lockedOutpoints: make(map[wire.OutPoint]struct{}),
		ctx:             ctx,
		cancelCtx:       cancelCtx,
	}

	// Finally, create the keyring using the conventions for remote
	// wallets.
	dcrw.remoteWalletKeyRing, err = newRemoteWalletKeyRing(acctXPriv, cfg.DB, dcrw)
	if err != nil {
		// Sign operations will fail, so signal the error and prevent
		// the wallet from considering itself synced (to prevent usage)
		return nil, fmt.Errorf("unable to create wallet key ring: %v", err)
	}

	return dcrw, nil
}

// BackEnd returns the underlying ChainService's name as a string.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) BackEnd() string {
	return "remotedcrwallet"
}

// Start initializes the underlying rpc connection, the wallet itself, and
// begins syncing to the current available blockchain state.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) Start() error {
	b.synced()
	return nil
}

// Stop signals the wallet for shutdown. Shutdown may entail closing
// any active sockets, database handles, stopping goroutines, etc.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) Stop() error {
	b.cancelCtx()
	return b.conn.Close()
}

// ConfirmedBalance returns the sum of all the wallet's unspent outputs that
// have at least confs confirmations. If confs is set to zero, then all unspent
// outputs, including those currently in the mempool will be included in the
// final sum.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ConfirmedBalance(confs int32) (dcrutil.Amount, error) {
	req := &pb.BalanceRequest{
		AccountNumber:         b.account,
		RequiredConfirmations: confs,
	}
	resp, err := b.wallet.Balance(context.Background(), req)
	if err != nil {
		return 0, err
	}
	return dcrutil.Amount(resp.Spendable), nil
}

// NewAddress returns the next external or internal address for the wallet
// dictated by the value of the `change` parameter. If change is true, then an
// internal address will be returned, otherwise an external address should be
// returned.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) NewAddress(t lnwallet.AddressType, change bool) (stdaddr.Address, error) {

	switch t {
	case lnwallet.PubKeyHash:
		// nop
	default:
		return nil, fmt.Errorf("unknown address type")
	}

	kind := pb.NextAddressRequest_BIP0044_EXTERNAL
	if change {
		kind = pb.NextAddressRequest_BIP0044_INTERNAL
	}
	req := &pb.NextAddressRequest{
		Kind:      kind,
		Account:   b.account,
		GapPolicy: pb.NextAddressRequest_GAP_POLICY_WRAP,
	}
	resp, err := b.wallet.NextAddress(context.Background(), req)
	if err != nil {
		return nil, err
	}

	addr, err := stdaddr.DecodeAddress(resp.Address, b.chainParams)
	if err != nil {
		return nil, err
	}
	return addr, nil
}

// LastUnusedAddress returns the last *unused* address known by the wallet. An
// address is unused if it hasn't received any payments. This can be useful in
// UIs in order to continually show the "freshest" address without having to
// worry about "address inflation" caused by continual refreshing. Similar to
// NewAddress it can derive a specified address type, and also optionally a
// change address.
func (b *DcrWallet) LastUnusedAddress(addrType lnwallet.AddressType) (
	stdaddr.Address, error) {

	switch addrType {
	case lnwallet.PubKeyHash:
		// nop
	default:
		return nil, fmt.Errorf("unknown address type")
	}

	return nil, fmt.Errorf("LastUnusedAddress unimplemented")
}

// IsOurAddress checks if the passed address belongs to this wallet
//
// This is a part of the WalletController interface.
func (b *DcrWallet) IsOurAddress(a stdaddr.Address) bool {
	validReq := &pb.ValidateAddressRequest{
		Address: a.String(),
	}
	validResp, err := b.wallet.ValidateAddress(context.Background(), validReq)
	if err != nil {
		dcrwLog.Errorf("Error validating address to determine "+
			"ownership: %v", err)
		return false
	}
	return validResp.IsMine
}

// SendOutputs funds, signs, and broadcasts a Decred transaction paying out to
// the specified outputs. In the case the wallet has insufficient funds, or the
// outputs are non-standard, a non-nil error will be returned.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) SendOutputs(outputs []*wire.TxOut,
	feeRate chainfee.AtomPerKByte, label string) (*wire.MsgTx, error) {

	ctxb := context.Background()

	reqOutputs := make([]*pb.ConstructTransactionRequest_Output, len(outputs))
	for i, out := range outputs {
		dest := &pb.ConstructTransactionRequest_OutputDestination{
			Script:        out.PkScript,
			ScriptVersion: uint32(out.Version),
		}
		reqOutputs[i] = &pb.ConstructTransactionRequest_Output{
			Amount:      out.Value,
			Destination: dest,
		}
	}
	req := &pb.ConstructTransactionRequest{
		SourceAccount:    b.account,
		FeePerKb:         int32(feeRate),
		NonChangeOutputs: reqOutputs,
	}

	resp, err := b.wallet.ConstructTransaction(ctxb, req)
	if err != nil {
		return nil, err
	}

	tx := new(wire.MsgTx)
	err = tx.FromBytes(resp.UnsignedTransaction)
	if err != nil {
		return nil, err
	}

	// We need to manually sign the transaction here (instead of passing it
	// to SignTransaction) because we don't hang onto the wallet password,
	// but we do know the master priv key to the source account and can
	// therefore derive the private keys for the individual addresses of
	// the selected utxos.
	//
	// Additionally, we need to retrieve the index of each utxo address to
	// know how to derive the private key.
	//
	// This ends up being harder (and slower) than this needs to be but
	// allows us to not have to keep a completely unlocked remote wallet.

	// Loop over the inputs which we need to sign.
	for i, in := range tx.TxIn {
		// Find out the PKScript of the input.
		txReq := &pb.GetTransactionRequest{
			TransactionHash: in.PreviousOutPoint.Hash[:],
		}
		txResp, err := b.wallet.GetTransaction(ctxb, txReq)
		if err != nil {
			return nil, fmt.Errorf("unable to fetch tx: %v", err)
		}

		var credit *pb.TransactionDetails_Output
		for _, c := range txResp.Transaction.Credits {
			if c.Index == in.PreviousOutPoint.Index {
				credit = c
				break
			}
		}
		if credit == nil {
			return nil, fmt.Errorf("unable to find pkscript of "+
				"prev outpoint %v", in.PreviousOutPoint)
		}
		pkScript := credit.OutputScript

		// Find out the HD index of the address.
		validReq := &pb.ValidateAddressRequest{
			Address: credit.Address,
		}
		validResp, err := b.wallet.ValidateAddress(ctxb, validReq)
		if err != nil {
			return nil, err
		}

		// Derive the private key that signs this utxo.
		branchXPriv := b.branchExtXPriv
		if validResp.IsInternal {
			branchXPriv = b.branchIntXPriv
		}
		extPrivKey, err := branchXPriv.Child(validResp.Index)
		if err != nil {
			return nil, err
		}
		privKey, err := extPrivKey.SerializedPrivKey()
		if err != nil {
			return nil, err
		}

		// Actually sign the input.
		sigScript, err := sign.SignatureScript(
			tx, i, pkScript, txscript.SigHashAll, privKey,
			dcrec.STEcdsaSecp256k1, true,
		)
		if err != nil {
			return nil, err
		}
		in.SignatureScript = sigScript
	}

	// Now publish the transaction to the network.
	signedTx, err := tx.Bytes()
	if err != nil {
		return nil, err
	}
	publishReq := &pb.PublishTransactionRequest{
		SignedTransaction: signedTx,
	}
	_, err = b.wallet.PublishTransaction(ctxb, publishReq)
	if err != nil {
		return nil, err
	}

	return tx, nil
}

// CreateSimpleTx creates a Bitcoin transaction paying to the specified
// outputs. The transaction is not broadcasted to the network, but a new change
// address might be created in the wallet database. In the case the wallet has
// insufficient funds, or the outputs are non-standard, an error should be
// returned. This method also takes the target fee expressed in sat/kw that
// should be used when crafting the transaction.
//
// NOTE: The dryRun argument can be set true to create a tx that doesn't alter
// the database. A tx created with this set to true SHOULD NOT be broadcasted.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) EstimateTxFee(outputs []*wire.TxOut,
	feeRate chainfee.AtomPerKByte) (fee int64, err error) {

	// TODO(decred) Review semantics for btcwallet's CreateSimpleTx.
	return 0, fmt.Errorf("CreateSimpleTx unimplemented for dcrwallet")
}

// LockOutpoint marks an outpoint as locked meaning it will no longer be deemed
// as eligible for coin selection. Locking outputs are utilized in order to
// avoid race conditions when selecting inputs for usage when funding a
// channel.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) LockOutpoint(o wire.OutPoint) {
	b.lockedOutpointsMu.Lock()
	b.lockedOutpoints[o] = struct{}{}
	b.lockedOutpointsMu.Unlock()
}

// UnlockOutpoint unlocks a previously locked output, marking it eligible for
// coin selection.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) UnlockOutpoint(o wire.OutPoint) {
	b.lockedOutpointsMu.Lock()
	delete(b.lockedOutpoints, o)
	b.lockedOutpointsMu.Unlock()
}

// ListUnspentWitness returns a slice of all the unspent outputs the wallet
// controls which pay to witness programs either directly or indirectly.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ListUnspentWitness(minConfs, maxConfs int32) (
	[]*lnwallet.Utxo, error) {

	if maxConfs != 0 && maxConfs != math.MaxInt32 {
		return nil, fmt.Errorf("maxconfs is not supported")
	}

	req := &pb.UnspentOutputsRequest{
		Account:               b.account,
		RequiredConfirmations: minConfs,
	}
	stream, err := b.wallet.UnspentOutputs(context.Background(), req)
	if err != nil {
		return nil, err
	}

	// Decred only supports p2pkh in its wallets.
	addressType := lnwallet.PubKeyHash

	// Convert to the appropriate format.
	utxos := make([]*lnwallet.Utxo, 0)
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		txid, err := chainhash.NewHash(msg.TransactionHash)
		if err != nil {
			return nil, err
		}

		// Ensure this utxo hasn't been locked.
		outp := wire.OutPoint{
			Hash:  *txid,
			Index: msg.OutputIndex,
			Tree:  int8(msg.Tree),
		}
		b.lockedOutpointsMu.Lock()
		_, lockedUtxo := b.lockedOutpoints[outp]
		b.lockedOutpointsMu.Unlock()
		if lockedUtxo {
			continue
		}

		// TODO(decred): Modify UnspentOutputs() grpc call to return
		// the confirmation height so that the confirmation count can
		// be deduced without having to perform a second rpc call.
		txReq := &pb.GetTransactionRequest{
			TransactionHash: txid[:],
		}
		txResp, err := b.wallet.GetTransaction(context.Background(), txReq)
		if err != nil {
			return nil, err
		}
		confs := txResp.Confirmations
		if confs > maxConfs {
			continue
		}

		utxo := &lnwallet.Utxo{
			AddressType:   addressType,
			Value:         dcrutil.Amount(msg.Amount),
			PkScript:      msg.PkScript,
			OutPoint:      outp,
			Confirmations: int64(confs),
		}
		utxos = append(utxos, utxo)
	}

	return utxos, nil
}

// PublishTransaction performs cursory validation (dust checks, etc), then
// finally broadcasts the passed transaction to the Decred network. If
// publishing the transaction fails, an error describing the reason is
// returned (currently ErrDoubleSpend). If the transaction is already
// published to the network (either in the mempool or chain) no error
// will be returned.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) PublishTransaction(tx *wire.MsgTx, label string) error {
	rawTx, err := tx.Bytes()
	if err != nil {
		return err
	}
	publishReq := &pb.PublishTransactionRequest{
		SignedTransaction: rawTx,
	}
	_, err = b.wallet.PublishTransaction(context.Background(), publishReq)
	dcrwLog.Debugf("PublishTransaction(%s): error %v", newLogClosure(
		func() string { return tx.TxHash().String() }), err)
	if err != nil {
		// TODO(decred): review if the string messages are correct.
		// Possible convert from checking the message to checking the
		// op.
		//
		// NOTE(decred): These checks were removed upstream due to
		// changing the underlying btcwallet semantics on
		// PublishTransaction().
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
		if strings.Contains(err.Error(), "already spends the same coins") {
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

// AbandonDoubleSpends abandons any unconfirmed transaction that also spends
// any of the specified outpoints.
//
// This is part of the WalletController interface.
func (b *DcrWallet) AbandonDoubleSpends(spentOutpoints ...*wire.OutPoint) error {

	// Fetch all unconfirmed transactions.
	req := &pb.GetTransactionsRequest{
		StartingBlockHeight: -1,
		EndingBlockHeight:   0,
	}

	stream, err := b.wallet.GetTransactions(context.Background(), req)
	if err != nil {
		return err
	}

	// Make a map of inputs that were spent to speed up lookup.
	spent := make(map[wire.OutPoint]struct{}, len(spentOutpoints))
	for _, outp := range spentOutpoints {
		spent[*outp] = struct{}{}
	}

	// Now collect all txs that need to be abandoned.
	abandon := make(map[chainhash.Hash]struct{}, len(spentOutpoints))
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		for _, tx := range msg.UnminedTransactions {
			wireTx := new(wire.MsgTx)
			err := wireTx.FromBytes(tx.Transaction)
			if err != nil {
				dcrwLog.Warnf("Error decoding wallet-provided "+
					"tx: %v", err)
				continue
			}
			txh := wireTx.TxHash()

			for _, in := range wireTx.TxIn {
				if _, isSpent := spent[in.PreviousOutPoint]; !isSpent {
					continue
				}

				// This input was spent. Register this as a tx
				// that needs abandoning.
				abandon[txh] = struct{}{}
				break
			}
		}
	}

	// Finally, abandon all transactions.
	for txh := range abandon {
		dcrwLog.Infof("Abandoning double spent tx %s", txh)
		req := &pb.AbandonTransactionRequest{
			TransactionHash: txh[:],
		}
		_, err := b.wallet.AbandonTransaction(context.Background(), req)
		if err != nil {
			dcrwLog.Warnf("Error abandoning tx %s: %v", txh, err)
		}
	}

	return nil
}

// extractBalanceDelta extracts the net balance delta from the PoV of the
// wallet given a TransactionSummary.
func extractBalanceDelta(
	txSummary *pb.TransactionDetails,
	tx *wire.MsgTx,
) (dcrutil.Amount, error) {
	// For each input we debit the wallet's outflow for this transaction,
	// and for each output we credit the wallet's inflow for this
	// transaction.
	var balanceDelta dcrutil.Amount
	for _, input := range txSummary.Debits {
		balanceDelta -= dcrutil.Amount(input.PreviousAmount)
	}
	for _, output := range txSummary.Credits {
		balanceDelta += dcrutil.Amount(tx.TxOut[output.Index].Value)
	}

	return balanceDelta, nil
}

// minedTransactionsToDetails is a helper function which converts a summary
// information about mined transactions to a TransactionDetail.
func minedTransactionsToDetails(
	currentHeight int32,
	block *pb.BlockDetails,
	chainParams *chaincfg.Params,
) ([]*lnwallet.TransactionDetail, error) {

	headerHeight := block.Height

	blockHash, err := chainhash.NewHash(block.Hash)
	if err != nil {
		return nil, err
	}

	details := make([]*lnwallet.TransactionDetail, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		wireTx := &wire.MsgTx{}
		txReader := bytes.NewReader(tx.Transaction)

		if err := wireTx.Deserialize(txReader); err != nil {
			return nil, err
		}

		var destAddresses []stdaddr.Address
		for _, txOut := range wireTx.TxOut {
			_, outAddresses := stdscript.ExtractAddrs(
				txOut.Version, txOut.PkScript, chainParams)
			destAddresses = append(destAddresses, outAddresses...)
		}

		txDetail := &lnwallet.TransactionDetail{
			Hash:             wireTx.TxHash(),
			NumConfirmations: currentHeight - headerHeight + 1,
			BlockHash:        blockHash,
			BlockHeight:      headerHeight,
			Timestamp:        block.Timestamp,
			TotalFees:        tx.Fee,
			DestAddresses:    destAddresses,
			RawTx:            tx.Transaction,
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
	summary *pb.TransactionDetails,
	chainParams *chaincfg.Params,
) (*lnwallet.TransactionDetail, error) {

	wireTx := &wire.MsgTx{}
	txReader := bytes.NewReader(summary.Transaction)

	if err := wireTx.Deserialize(txReader); err != nil {
		return nil, err
	}

	var destAddresses []stdaddr.Address
	for _, txOut := range wireTx.TxOut {
		_, outAddresses :=
			stdscript.ExtractAddrs(txOut.Version,
				txOut.PkScript, chainParams)
		destAddresses = append(destAddresses, outAddresses...)
	}

	txDetail := &lnwallet.TransactionDetail{
		Hash:          wireTx.TxHash(),
		TotalFees:     summary.Fee,
		Timestamp:     summary.Timestamp,
		DestAddresses: destAddresses,
		RawTx:         summary.Transaction,
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
func (b *DcrWallet) ListTransactionDetails(startHeight,
	endHeight int32) ([]*lnwallet.TransactionDetail, error) {

	// Grab the best block the wallet knows of, we'll use this to calculate
	// # of confirmations shortly below.
	bestBlockRes, err := b.wallet.BestBlock(context.Background(), &pb.BestBlockRequest{})
	if err != nil {
		return nil, err
	}
	currentHeight := int32(bestBlockRes.Height)

	req := &pb.GetTransactionsRequest{
		StartingBlockHeight: startHeight,
		EndingBlockHeight:   endHeight,
	}

	stream, err := b.wallet.GetTransactions(context.Background(), req)
	if err != nil {
		return nil, err
	}
	txs := make([]*lnwallet.TransactionDetail, 0)
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if msg.MinedTransactions != nil {
			minedTxs, err := minedTransactionsToDetails(currentHeight,
				msg.MinedTransactions, b.chainParams)
			if err != nil {
				return nil, err
			}
			txs = append(txs, minedTxs...)
		}

		for _, tx := range msg.UnminedTransactions {
			unminedTx, err := unminedTransactionsToDetail(tx, b.chainParams)
			if err != nil {
				return nil, err
			}
			txs = append(txs, unminedTx)
		}
	}

	return txs, nil
}

// txSubscriptionClient encapsulates the transaction notification client from
// the base wallet. Notifications received from the client will be proxied over
// two distinct channels.
type txSubscriptionClient struct {
	txClient pb.WalletService_TransactionNotificationsClient

	confirmed   chan *lnwallet.TransactionDetail
	unconfirmed chan *lnwallet.TransactionDetail

	wallet      pb.WalletServiceClient
	chainParams *chaincfg.Params

	wg     sync.WaitGroup
	ctx    context.Context
	cancel func()
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
	t.cancel()
	t.wg.Wait()
}

// notificationProxier proxies the notifications received by the underlying
// wallet's notification client to a higher-level TransactionSubscription
// client.
func (t *txSubscriptionClient) notificationProxier() {
	for {
		msg, err := t.txClient.Recv()
		if err == io.EOF {
			// Cancel() was called.
			break
		}
		if err != nil {
			dcrwLog.Errorf("Error during tx subscription: %v", err)
			break
		}

		// TODO(roasbeef): handle detached blocks
		ctxb := context.Background()
		bestBlockResp, err := t.wallet.BestBlock(ctxb, &pb.BestBlockRequest{})
		if err != nil {
			dcrwLog.Errorf("Unable to query best block in tx subscription")
		}
		currentHeight := int32(bestBlockResp.Height)

		// Launch a goroutine to re-package and send notifications for
		// any newly confirmed transactions.
		go func() {
			for _, block := range msg.AttachedBlocks {
				details, err := minedTransactionsToDetails(
					currentHeight, block, t.chainParams,
				)
				if err != nil {
					continue
				}

				for _, d := range details {
					select {
					case t.confirmed <- d:
					case <-t.ctx.Done():
						return
					}
				}
			}

		}()

		// Launch a goroutine to re-package and send
		// notifications for any newly unconfirmed transactions.
		go func() {
			for _, tx := range msg.UnminedTransactions {
				detail, err := unminedTransactionsToDetail(
					tx, t.chainParams,
				)
				if err != nil {
					continue
				}

				select {
				case t.unconfirmed <- detail:
				case <-t.ctx.Done():
					return
				}
			}
		}()
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
	req := &pb.TransactionNotificationsRequest{}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := b.wallet.TransactionNotifications(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}

	txClient := &txSubscriptionClient{
		txClient:    stream,
		confirmed:   make(chan *lnwallet.TransactionDetail),
		unconfirmed: make(chan *lnwallet.TransactionDetail),
		wallet:      b.wallet,
		chainParams: b.chainParams,
		ctx:         ctx,
		cancel:      cancel,
	}
	txClient.wg.Add(1)
	go txClient.notificationProxier()

	return txClient, nil
}

// IsSynced returns a boolean indicating if from the PoV of the wallet, it has
// fully synced to the current best block in the main chain.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) IsSynced() (bool, int64, error) {
	// Grab the best chain state the wallet is currently aware of.
	ctxb := context.Background()
	bestBlockResp, err := b.wallet.BestBlock(ctxb, &pb.BestBlockRequest{})
	if err != nil {
		return false, 0, err
	}
	walletBestHash := bestBlockResp.Hash
	blockInfoResp, err := b.wallet.BlockInfo(ctxb, &pb.BlockInfoRequest{BlockHash: walletBestHash})
	if err != nil {
		return false, 0, err
	}
	headerTS := time.Unix(blockInfoResp.Timestamp, 0)

	// TODO(decred) Check if the wallet is still syncing.  This is
	// currently done by checking the associated chainIO but ideally the
	// wallet should return the height it's attempting to sync to.
	if b.cfg.ChainIO != nil {
		ioHash, _, err := b.cfg.ChainIO.GetBestBlock()
		if err != nil {
			return false, 0, err
		}
		if !bytes.Equal(walletBestHash, ioHash[:]) {
			return false, headerTS.Unix(), nil
		}
	}

	// If the timestamp on the best header is more than 2 hours in the
	// past, then we're not yet synced.
	minus2Hours := time.Now().Add(-2 * time.Hour)
	if headerTS.Before(minus2Hours) {
		return false, headerTS.Unix(), nil
	}

	// Check if the wallet has completed the initial sync procedure (discover
	// addresses, load tx filter, etc).
	walletSynced := atomic.LoadUint32(&b.atomicWalletSynced) == 1

	return walletSynced, headerTS.Unix(), nil
}

func (b *DcrWallet) BestBlock() (int64, chainhash.Hash, int64, error) {
	ctxb := context.Background()
	bestBlockResp, err := b.wallet.BestBlock(ctxb, &pb.BestBlockRequest{})
	if err != nil {
		return 0, chainhash.Hash{}, 0, err
	}
	walletBestHash, err := chainhash.NewHash(bestBlockResp.Hash)
	if err != nil {
		return 0, chainhash.Hash{}, 0, err
	}
	blockInfoResp, err := b.wallet.BlockInfo(ctxb, &pb.BlockInfoRequest{BlockHash: walletBestHash[:]})
	if err != nil {
		return 0, chainhash.Hash{}, 0, err
	}
	headerTS := time.Unix(blockInfoResp.Timestamp, 0)

	return int64(bestBlockResp.Height), *walletBestHash, headerTS.Unix(), nil
}

// InitialSyncChannel returns the channel used to signal that wallet init has
// finished.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) InitialSyncChannel() <-chan struct{} {
	return b.syncedChan
}

// synced is called by the chosen chain syncer (rpc/spv) after the wallet has
// successfully synced its state to the chain.
func (b *DcrWallet) synced() {
	dcrwLog.Debug("Syncer notified wallet is synced")

	// Signal that the wallet is synced by closing the channel.
	if atomic.CompareAndSwapUint32(&b.atomicWalletSynced, 0, 1) {
		close(b.syncedChan)
	}
}

// Bip44AddressInfo returns the BIP44 relevant (account, branch and index) of
// the given wallet address.
func (b *DcrWallet) Bip44AddressInfo(addr stdaddr.Address) (uint32, uint32, uint32, error) {
	req := &pb.ValidateAddressRequest{
		Address: addr.String(),
	}
	resp, err := b.wallet.ValidateAddress(context.Background(), req)
	if err != nil {
		return 0, 0, 0, err
	}

	if !resp.IsValid {
		return 0, 0, 0, fmt.Errorf("invalid address")
	}
	if !resp.IsMine {
		return 0, 0, 0, fmt.Errorf("not an owned address")
	}
	if resp.IsScript {
		return 0, 0, 0, fmt.Errorf("not a p2pkh address")
	}

	branch := uint32(0)
	if resp.IsInternal {
		branch = 1
	}
	return resp.AccountNumber, branch, resp.Index, nil
}

// LabelTransaction adds the given external label to the specified transaction.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) LabelTransaction(hash chainhash.Hash, label string, overwrite bool) error {
	return fmt.Errorf("unimplemented")
}

// LeaseOutput markes the output as used for some time.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) LeaseOutput(lnwallet.LockID, wire.OutPoint) (time.Time, error) {
	return time.Time{}, fmt.Errorf("unimplemented")
}

// ReleaseOutput marks the output as unused.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) ReleaseOutput(lnwallet.LockID, wire.OutPoint) error {
	return fmt.Errorf("unimplemented")
}

// GetRecoveryInfo returns the current status of the recovery of the wallet.
//
// This is a part of the WalletController interface.
func (b *DcrWallet) GetRecoveryInfo() (bool, float64, error) {
	return false, 0, fmt.Errorf("unimplemented")
}
