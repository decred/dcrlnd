package dcrwallet

import (
	"context"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"github.com/decred/dcrlnd/lnwallet"

	"decred.org/dcrwallet/v5/errors"
	"decred.org/dcrwallet/v5/wallet"
)

// Compile time check to ensure DcrWallet fulfills lnwallet.BlockChainIO.
var _ lnwallet.BlockChainIO = (*DcrWallet)(nil)

// GetBestBlock returns the current height and hash of the best known block
// within the main chain.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBestBlock() (*chainhash.Hash, int32, error) {
	bh, h := b.wallet.MainChainTip(b.ctx)
	return &bh, h, nil
}

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
			dcrwLog.Errorf("Dcrwallet error while running %s: %v", name, err)
		}
	}()
}

// GetUtxoWithHistorical returns the original output referenced by the passed
// outpoint that created the target pkScript, searched for using the passed
// historical chain scanner.
func GetUtxoWithHistorical(ctx context.Context, historical *chainscan.Historical, op *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*wire.TxOut, error) {
	scriptVersion := uint16(0)
	confirmCompleted := make(chan struct{})
	spendCompleted := make(chan struct{})
	var confirmOut *wire.TxOut
	var spent *chainscan.Event

	// Use a context to stop the search once both a spend and confirm have
	// been found.
	searchCtx, searchCancel := context.WithCancel(ctx)
	defer searchCancel()

	dcrwLog.Debugf("GetUtxo looking for %s start at %d", op, heightHint)

	foundSpend := func(e chainscan.Event, _ chainscan.FindFunc) {
		dcrwLog.Debugf("Found spend of %s on block %d (%s) for GetUtxo",
			op, e.BlockHeight, e.BlockHash)
		spent = &e
		searchCancel() // Found the spend, stop searching.
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
			chainscan.WithCancelChan(searchCtx.Done()),
			chainscan.WithCompleteChan(spendCompleted),
		)
		if err != nil {
			dcrwLog.Warnf("Unable to chainscan for spend of UTXO: %v", err)
		}
	}

	// First search for the confirmation of the given script, then for its
	// spending.
	historical.Find(
		chainscan.ConfirmedOutPoint(*op, scriptVersion, pkScript),
		chainscan.WithStartHeight(int32(heightHint)),
		chainscan.WithCancelChan(searchCtx.Done()),
		chainscan.WithFoundCallback(foundConfirm),
		chainscan.WithCompleteChan(confirmCompleted),
	)

	for confirmCompleted != nil && spendCompleted != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-searchCtx.Done():
			// Spend found.
			confirmCompleted = nil
			spendCompleted = nil

		case <-confirmCompleted:
			confirmCompleted = nil
			if confirmOut == nil {
				spendCompleted = nil
			}

		case <-spendCompleted:
			spendCompleted = nil
		}
	}

	switch {
	case spent != nil:
		return nil, lnwallet.ErrUtxoAlreadySpent{
			PrevOutPoint: *op,
			BlockHash:    spent.BlockHash,
			BlockHeight:  spent.BlockHeight,
			TxIndex:      spent.TxIndex,
			SpendingOutPoint: wire.OutPoint{
				Hash:  *spent.Tx.CachedTxHash(),
				Index: uint32(spent.Index),
				Tree:  spent.Tree,
			},
		}
	case confirmOut != nil:
		return confirmOut, nil
	default:
		return nil, errors.Errorf("output %s not found during chain scan", op)
	}

}

// GetUtxo returns the original output referenced by the passed outpoint that
// created the target pkScript.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetUtxo(op *wire.OutPoint, pkScript []byte,
	heightHint uint32, cancel <-chan struct{}) (*wire.TxOut, error) {

	// Setup a context that is canceled when either the wallet or the passed
	// cancelChan are canceled.
	ctx, cancelCtx := context.WithCancel(b.ctx)
	defer cancelCtx()
	go func() {
		select {
		case <-cancel:
			cancelCtx()
		case <-ctx.Done():
		}
	}()

	src := csdrivers.NewDcrwalletCSDriver(b.wallet, b.cfg.BlockCache)
	historical := chainscan.NewHistorical(src)
	runAndLogOnError(ctx, src.Run, "GetUtxo.DcrwalletCSDriver")
	runAndLogOnError(ctx, historical.Run, "GetUtxo.Historical")

	return GetUtxoWithHistorical(ctx, historical, op, pkScript, heightHint)
}

// GetBlock returns a raw block from the server given its hash.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	// TODO: unify with the driver on chainscan.
	ctx := b.ctx

getblock:
	for {
		// Keep trying to get the network backend until the context is
		// canceled.
		n, err := b.wallet.NetworkBackend()
		if errors.Is(err, errors.NoPeers) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				continue getblock
			}
		}

		blocks, err := n.Blocks(ctx, []*chainhash.Hash{blockHash})
		if len(blocks) > 0 && err == nil {
			return blocks[0], nil
		}

		// The syncer might have failed due to any number of reasons,
		// but it's likely it will come back online shortly. So wait
		// until we can try again.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}

}

// GetBlockHash returns the hash of the block in the best blockchain at the
// given height.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBlockHash(blockHeight int64) (*chainhash.Hash, error) {
	id := wallet.NewBlockIdentifierFromHeight(int32(blockHeight))
	bl, err := b.wallet.BlockInfo(b.ctx, id)
	if err != nil {
		return nil, err
	}

	return &bl.Hash, err
}
