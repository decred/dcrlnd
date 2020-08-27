package mock

import (
	"sync"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/wire"
	"golang.org/x/exp/slices"

	"github.com/decred/dcrlnd/chainntnfs"
)

// SpendNtnfRequest  tracks calls made to the mock notifier RegisterSpendNtfn.
//
// Note: this exists only in dcrlnd.
type SpendNtnfRequest struct {
	Outpoint wire.OutPoint
	PkScript []byte
}

// ChainNotifier is a mock implementation of the ChainNotifier interface.
type ChainNotifier struct {
	SpendChan chan *chainntnfs.SpendDetail
	EpochChan chan *chainntnfs.BlockEpoch
	ConfChan  chan *chainntnfs.TxConfirmation

	mtx        sync.Mutex
	spendNtfns []SpendNtnfRequest
}

// RegisteredSpendNtfns returns registered spend notifications. This is only
// part of the mock interface.
func (c *ChainNotifier) RegisteredSpendNtfns() []SpendNtnfRequest {
	c.mtx.Lock()
	res := slices.Clone(c.spendNtfns)
	c.mtx.Unlock()
	return res
}

// RegisterConfirmationsNtfn returns a ConfirmationEvent that contains a channel
// that the tx confirmation will go over.
func (c *ChainNotifier) RegisterConfirmationsNtfn(txid *chainhash.Hash,
	pkScript []byte, numConfs, heightHint uint32) (*chainntnfs.ConfirmationEvent,
	error) {

	return &chainntnfs.ConfirmationEvent{
		Confirmed: c.ConfChan,
		Cancel:    func() {},
	}, nil
}

// RegisterSpendNtfn returns a SpendEvent that contains a channel that the spend
// details will go over.
func (c *ChainNotifier) RegisterSpendNtfn(outpoint *wire.OutPoint,
	pkScript []byte, heightHint uint32) (*chainntnfs.SpendEvent, error) {

	c.mtx.Lock()
	c.spendNtfns = append(c.spendNtfns, SpendNtnfRequest{
		Outpoint: *outpoint,
		PkScript: pkScript,
	})
	c.mtx.Unlock()

	return &chainntnfs.SpendEvent{
		Spend:  c.SpendChan,
		Cancel: func() {},
	}, nil
}

// RegisterBlockEpochNtfn returns a BlockEpochEvent that contains a channel that
// block epochs will go over.
func (c *ChainNotifier) RegisterBlockEpochNtfn(blockEpoch *chainntnfs.BlockEpoch) (
	*chainntnfs.BlockEpochEvent, error) {

	return &chainntnfs.BlockEpochEvent{
		Epochs: c.EpochChan,
		Cancel: func() {},
	}, nil
}

// Start currently returns a dummy value.
func (c *ChainNotifier) Start() error {
	return nil
}

// Started currently returns a dummy value.
func (c *ChainNotifier) Started() bool {
	return true
}

// Stop currently returns a dummy value.
func (c *ChainNotifier) Stop() error {
	return nil
}
