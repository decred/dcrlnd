package remotedcrwallet

import (
	"context"
	"time"

	"decred.org/dcrwallet/v5/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
	"github.com/decred/dcrlnd/lnwallet"
	"github.com/decred/dcrlnd/lnwallet/dcrwallet"

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

	src := csdrivers.NewRemoteWalletCSDriver(b.wallet, b.network, b.cfg.BlockCache)
	historical := chainscan.NewHistorical(src)
	runAndLogOnError(ctx, src.Run, "GetUtxo.RemoteWalletCSDriver")
	runAndLogOnError(ctx, historical.Run, "GetUtxo.Historical")

	return dcrwallet.GetUtxoWithHistorical(ctx, historical, op, pkScript, heightHint)
}

// GetBlock returns a raw block from the server given its hash.
//
// This method is a part of the lnwallet.BlockChainIO interface.
func (b *DcrWallet) GetBlock(blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
	return b.cfg.BlockCache.GetBlock(b.ctx, blockHash, b.getBlock)
}

func (b *DcrWallet) getBlock(ctx context.Context, blockHash *chainhash.Hash) (*wire.MsgBlock, error) {
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
		resp, err = b.network.GetRawBlock(ctx, req)
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
