package remotedcrwallet

import (
	"context"
	"time"

	"decred.org/dcrwallet/v2/errors"
	"decred.org/dcrwallet/v2/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"github.com/decred/dcrlnd/lnwallet"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Compile time check to ensure DcrWallet fulfills lnwallet.BlockChainIO.
var _ lnwallet.BlockChainIO = (*DcrWallet)(nil)

func runAndLogOnError(ctx context.Context, f func(context.Context) error, name string) {
	go func() {
		err := f(ctx)
		select {
		case <-ctx.Done():
			// Any errs were due to done() so, ok
			return
		default:
		}
		if err != nil {
			dcrwLog.Errorf("RemoteWallet error while running %s: %v", name, err)
		}
	}()
}

// GetBestBlock returns the current height and hash of the best known block
// within the main chain.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBestBlock() (*chainhash.Hash, int32, error) {
	resp, err := b.wallet.BestBlock(b.ctx, &walletrpc.BestBlockRequest{})
	if err != nil {
		return nil, 0, err
	}
	bh, err := chainhash.NewHash(resp.Hash)
	if err != nil {
		return nil, 0, err
	}
	return bh, int32(resp.Height), nil
}

// GetUtxo returns the original output referenced by the passed outpoint that
// create the target pkScript.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetUtxo(op *wire.OutPoint, pkScript []byte,
	heightHint uint32, cancel <-chan struct{}) (*wire.TxOut, error) {
	// FIXME: unify with the dcrwallet driver.
	src := csdrivers.NewRemoteWalletCSDriver(b.wallet, b.network)
	ctx, cancelCtx := context.WithCancel(b.ctx)
	defer cancelCtx()

	historical := chainscan.NewHistorical(src)
	scriptVersion := uint16(0)
	confirmCompleted := make(chan struct{})
	spendCompleted := make(chan struct{})
	var confirmOut *wire.TxOut
	var spent *chainscan.Event

	runAndLogOnError(ctx, src.Run, "GetUtxo.RemoteWalletCSDriver")
	runAndLogOnError(ctx, historical.Run, "GetUtxo.Historical")

	dcrwLog.Debugf("GetUtxo looking for %s start at %d", op, heightHint)

	foundSpend := func(e chainscan.Event, _ chainscan.FindFunc) {
		dcrwLog.Debugf("Found spend of %s on block %d (%s) for GetUtxo",
			op, e.BlockHeight, e.BlockHash)
		spent = &e
	}

	foundConfirm := func(e chainscan.Event, findExtra chainscan.FindFunc) {
		// Found confirmation of the outpoint. Try to find someone
		// spending it.
		confirmOut = e.Tx.TxOut[e.Index]
		dcrwLog.Debugf("Found confirmation of %s on block %d (%s) for GetUtxo",
			op, e.BlockHeight, e.BlockHash)
		err := findExtra(
			chainscan.SpentOutPoint(*op, scriptVersion, pkScript),
			chainscan.WithStartHeight(e.BlockHeight+1),
			chainscan.WithFoundCallback(foundSpend),
			chainscan.WithCancelChan(cancel),
			chainscan.WithCompleteChan(spendCompleted),
		)
		if err != nil {
			dcrwLog.Warnf("Unable to chainscan for spend of UTXO: %v:", err)
		}
	}

	// First search for the confirmation of the given script, then for its
	// spending.
	historical.Find(
		chainscan.ConfirmedOutPoint(*op, scriptVersion, pkScript),
		chainscan.WithStartHeight(int32(heightHint)),
		chainscan.WithCancelChan(cancel),
		chainscan.WithFoundCallback(foundConfirm),
		chainscan.WithCompleteChan(confirmCompleted),
	)

	for confirmCompleted != nil && spendCompleted != nil {
		select {
		case <-cancel:
			return nil, errors.New("GetUtxo cancelled by caller")
		case <-ctx.Done():
			return nil, errors.New("wallet shutting down")
		case <-confirmCompleted:
			confirmCompleted = nil
		case <-spendCompleted:
			spendCompleted = nil
		}
	}

	switch {
	case spent != nil:
		return nil, errors.Errorf("output %s spent by %s:%d", op,
			spent.Tx.CachedTxHash(), spent.Index)
	case confirmOut != nil:
		return confirmOut, nil
	default:
		return nil, errors.Errorf("output %s not found during chain scan", op)
	}
}

// GetBlock returns a raw block from the server given its hash.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	// TODO: unify with the driver on chainscan.
	var (
		resp *walletrpc.GetRawBlockResponse
		err  error
	)

	req := &walletrpc.GetRawBlockRequest{
		BlockHash: blockHash[:],
	}

	// If the response error code is 'Unavailable' it means the wallet
	// isn't connected to any peers while in SPV mode. In that case, wait a
	// bit and try again.
	for stop := false; !stop; {
		resp, err = b.network.GetRawBlock(b.ctx, req)
		switch {
		case status.Code(err) == codes.Unavailable:
			dcrwLog.Warnf("Network unavailable from wallet; will try again in 5 seconds")
			select {
			case <-b.ctx.Done():
				return nil, b.ctx.Err()
			case <-time.After(5 * time.Second):
			}
		case err != nil:
			return nil, err
		default:
			stop = true
		}
	}

	bl := &wire.MsgBlock{}
	err = bl.FromBytes(resp.Block)
	if err != nil {
		return nil, err
	}

	return bl, nil
}

// GetBlockHash returns the hash of the block in the best blockchain at the
// given height.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	req := &walletrpc.BlockInfoRequest{
		BlockHeight: int32(blockHeight),
	}
	resp, err := b.wallet.BlockInfo(b.ctx, req)
	if err != nil {
		return nil, err
	}
	return chainhash.NewHash(resp.BlockHash)
}
