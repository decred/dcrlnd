package dcrwallet

import (
	"context"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"github.com/decred/dcrlnd/lnwallet"

	"decred.org/dcrwallet/v3/errors"
	"decred.org/dcrwallet/v3/wallet"
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

// GetUtxo returns the original output referenced by the passed outpoint that
// created the target pkScript.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetUtxo(op *wire.OutPoint, pkScript []byte,
	heightHint uint32, cancel <-chan struct{}) (*wire.TxOut, error) {
	src := csdrivers.NewDcrwalletCSDriver(b.wallet)
	ctx, cancelCtx := context.WithCancel(b.ctx)
	defer cancelCtx()

	historical := chainscan.NewHistorical(src)
	scriptVersion := uint16(0)
	confirmCompleted := make(chan struct{})
	spendCompleted := make(chan struct{})
	var confirmOut *wire.TxOut
	var spent *chainscan.Event

	runAndLogOnError(ctx, src.Run, "GetUtxo.DcrwalletCSDriver")
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
			dcrwLog.Warnf("Unable to chainscan for spend of UTXO: %v", err)
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
