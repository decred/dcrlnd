package dcrwnotify

import (
	"context"

	"decred.org/dcrwallet/v4/wallet"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainntnfs"
	csnotify "github.com/decred/dcrlnd/chainntnfs/chainscannotify"
	"github.com/decred/dcrlnd/chainscan/csdrivers"
)

type DcrwChainSource struct {
	*csdrivers.DcrwalletCSDriver
	w *wallet.Wallet
}

func (s *DcrwChainSource) GetBlockHash(ctx context.Context, height int32) (*chainhash.Hash, error) {
	id := wallet.NewBlockIdentifierFromHeight(height)
	b, err := s.w.BlockInfo(ctx, id)
	if err != nil {
		return nil, err
	}

	return &b.Hash, err
}

func (s *DcrwChainSource) GetBlockHeader(ctx context.Context, hash *chainhash.Hash) (*wire.BlockHeader, error) {
	id := wallet.NewBlockIdentifierFromHash(hash)
	b, err := s.w.BlockInfo(ctx, id)
	if err != nil {
		return nil, err
	}

	var header wire.BlockHeader
	err = header.FromBytes(b.Header)
	if err != nil {
		return nil, err
	}

	return &header, err
}

func (s *DcrwChainSource) StoresReorgedHeaders() bool {
	return true
}

func NewDcrwChainSource(w *wallet.Wallet, bCache *blockcache.BlockCache) *DcrwChainSource {
	cs := csdrivers.NewDcrwalletCSDriver(w, bCache)
	return &DcrwChainSource{
		w:                 w,
		DcrwalletCSDriver: cs,
	}
}

var _ csnotify.ChainSource = (*DcrwChainSource)(nil)

// New returns a new DcrdNotifier instance. This function assumes the dcrd node
// detailed in the passed configuration is already running, and willing to
// accept new websockets clients.
func New(w *wallet.Wallet, chainParams *chaincfg.Params,
	spendHintCache chainntnfs.SpendHintCache,
	confirmHintCache chainntnfs.ConfirmHintCache,
	bCache *blockcache.BlockCache) (*csnotify.ChainscanNotifier, error) {

	src := NewDcrwChainSource(w, bCache)
	return csnotify.New(src, chainParams, spendHintCache, confirmHintCache)
}
