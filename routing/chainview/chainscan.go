package chainview

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/chainscan"
	"github.com/decred/dcrlnd/channeldb"
)

type chainscanChainSource interface {
	GetBlock(context.Context, *chainhash.Hash) (*wire.MsgBlock, error)
	CurrentTip(context.Context) (*chainhash.Hash, int32, error)

	ChainEvents(context.Context) <-chan chainscan.ChainEvent

	GetCFilter(context.Context, int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error)

	GetBlockHash(context.Context, int32) (*chainhash.Hash, error)
	GetBlockHeader(context.Context, *chainhash.Hash) (*wire.BlockHeader, error)

	Run(context.Context) error
}

// chainscanFilteredChainView is an implementation of the FilteredChainView
// interface which uses the chainscan package to perform its duties.
type chainscanFilteredChainView struct {
	started int32 // To be used atomically.
	stopped int32 // To be used atomically.

	// bestHeight is the height of the latest block added to the
	// blockQueue from the onFilteredConnectedMethod. It is used to
	// determine up to what height we would need to rescan in case
	// of a filter update.
	bestHeightMtx sync.Mutex
	bestHeight    int64

	// blockEventQueue is the ordered queue used to keep the order
	// of connected and disconnected blocks sent to the reader of the
	// chainView.
	blockQueue *blockEventQueue

	// filterUpdates is a channel in which updates to the utxo filter
	// attached to this instance are sent over.
	filterUpdates chan csFilterUpdate

	// chainFilter is the set of utox's that we're currently watching
	// spends for within the chain.
	//
	// It stores in the value the cancelChan that once closed removes the
	// specified outpoint from the tipWatcher.
	filterMtx   sync.RWMutex
	chainFilter map[wire.OutPoint]chan struct{}

	// filterBlockReqs is a channel in which requests to filter select
	// blocks will be sent over.
	filterBlockReqs chan *filterBlockReq

	chainSource   chainscanChainSource
	tipWatcher    *chainscan.TipWatcher
	chainEvents   <-chan chainscan.ChainEvent
	tipWatcherTxs map[chainhash.Hash]map[*wire.MsgTx]chainscan.Event

	ctx       context.Context
	cancelCtx func()

	quit chan struct{}
	wg   sync.WaitGroup
}

// A compile time check to ensure chainscanFilteredChainView implements the
// chainview.FilteredChainView.
var _ FilteredChainView = (*chainscanFilteredChainView)(nil)

// newChainscanFilteredChainView creates a new instance of a FilteredChainView
// from a compatible chain source implementation.
func newChainscanFilteredChainView(cs chainscanChainSource) (*chainscanFilteredChainView, error) {
	ctx, cancel := context.WithCancel(context.Background())

	tipWatcher := chainscan.NewTipWatcher(cs)
	chainView := &chainscanFilteredChainView{
		chainFilter:     make(map[wire.OutPoint]chan struct{}),
		filterUpdates:   make(chan csFilterUpdate),
		filterBlockReqs: make(chan *filterBlockReq),
		quit:            make(chan struct{}),
		blockQueue:      newBlockEventQueue(),

		chainSource:   cs,
		tipWatcher:    tipWatcher,
		chainEvents:   tipWatcher.ChainEvents(ctx),
		tipWatcherTxs: make(map[chainhash.Hash]map[*wire.MsgTx]chainscan.Event),

		ctx:       ctx,
		cancelCtx: cancel,
	}

	return chainView, nil
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
			log.Errorf("CSFilteredView error while running %s: %v", name, err)
		}
	}()
}

// Start starts all goroutines necessary for normal operation.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) Start() error {
	// Already started?
	if atomic.AddInt32(&b.started, 1) != 1 {
		return nil
	}

	log.Infof("ChainscanFilteredChainView starting")

	runAndLogOnError(b.ctx, b.chainSource.Run, "chainSource")
	runAndLogOnError(b.ctx, b.tipWatcher.Run, "tipWatcher")

	_, bestHeight, err := b.chainSource.CurrentTip(b.ctx)
	if err != nil {
		return err
	}

	b.bestHeightMtx.Lock()
	b.bestHeight = int64(bestHeight)
	b.bestHeightMtx.Unlock()

	b.blockQueue.Start()

	b.wg.Add(2)
	go b.handleChainEvents()
	go b.chainFilterer()

	return nil
}

// Stop stops all goroutines which we launched by the prior call to the Start
// method.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) Stop() error {
	// Already shutting down?
	if atomic.AddInt32(&b.stopped, 1) != 1 {
		return nil
	}

	// Shutdown chainscan services.
	b.cancelCtx()

	b.blockQueue.Stop()

	log.Infof("FilteredChainView stopping")

	close(b.quit)
	b.wg.Wait()

	return nil
}

func (b *chainscanFilteredChainView) foundAtTip(e chainscan.Event, _ chainscan.FindFunc) {
	log.Tracef("Found at tip bh %s: %s", e.BlockHash, e)
	txs, ok := b.tipWatcherTxs[e.BlockHash]
	if !ok {
		txs = make(map[*wire.MsgTx]chainscan.Event)
		b.tipWatcherTxs[e.BlockHash] = txs
	}
	txs[e.Tx] = e
}

// filterBlock filters the given block hash against a currently processed block
// by the tipWatcher.
//
// This removes any found spent utxos from the tipWatcher and the list of
// watched utxos.
func (b *chainscanFilteredChainView) filterBlock(bh *chainhash.Hash) []*wire.MsgTx {
	matches := b.tipWatcherTxs[*bh]
	delete(b.tipWatcherTxs, *bh)
	txs := make([]*wire.MsgTx, 0, len(matches))
	b.filterMtx.Lock()
	for _, m := range matches {
		txs = append(txs, m.Tx)
		outp := m.Tx.TxIn[m.Index].PreviousOutPoint
		if cancelChan, ok := b.chainFilter[outp]; ok {
			close(cancelChan)
			delete(b.chainFilter, outp)
		}
	}
	b.filterMtx.Unlock()

	return txs
}

// onBlockConnected is called for each block that's connected to the end of the
// main chain. Based on our current chain filter, the block may or may not
// include any relevant transactions.
func (b *chainscanFilteredChainView) onBlockConnected(e chainscan.BlockConnectedEvent) {
	txs := b.filterBlock(e.BlockHash())

	// We record the height of the last connected block added to the
	// blockQueue such that we can scan up to this height in case of a
	// rescan. It must be protected by a mutex since a filter update might
	// be trying to read it concurrently.
	b.bestHeightMtx.Lock()
	b.bestHeight = int64(e.Height)
	b.bestHeightMtx.Unlock()

	block := &FilteredBlock{
		Hash:         e.Hash,
		Height:       int64(e.Height),
		Transactions: txs,
	}

	b.blockQueue.Add(&blockEvent{
		eventType: connected,
		block:     block,
	})
}

// onBlockDisconnected is a callback which is executed once a block is
// disconnected from the end of the main chain.
func (b *chainscanFilteredChainView) onBlockDisconnected(e chainscan.BlockDisconnectedEvent) {
	log.Debugf("Got disconnected block at height %d: %s", e.Height,
		e.Hash)

	filteredBlock := &FilteredBlock{
		Hash:   e.Hash,
		Height: int64(e.Height),
	}

	b.blockQueue.Add(&blockEvent{
		eventType: disconnected,
		block:     filteredBlock,
	})
}

func (b *chainscanFilteredChainView) handleChainEvents() {
	defer b.wg.Done()

	for {
		var ce chainscan.ChainEvent
		select {
		case <-b.ctx.Done():
			return
		case ce = <-b.chainEvents:
		}

		switch e := ce.(type) {
		case chainscan.BlockConnectedEvent:
			b.onBlockConnected(e)
		case chainscan.BlockDisconnectedEvent:
			b.onBlockDisconnected(e)
		default:
			log.Warnf("Unknown block event: %t", ce)
		}
	}
}

// FilterBlock takes a block hash, and returns a FilteredBlocks which is the
// result of applying the current registered UTXO sub-set on the block
// corresponding to that block hash. If any watched UTOX's are spent by the
// selected lock, then the internal chainFilter will also be updated.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) FilterBlock(blockHash *chainhash.Hash) (*FilteredBlock, error) {
	req := &filterBlockReq{
		blockHash: blockHash,
		resp:      make(chan *FilteredBlock, 1),
		err:       make(chan error, 1),
	}

	select {
	case b.filterBlockReqs <- req:
	case <-b.quit:
		return nil, fmt.Errorf("FilteredChainView shutting down")
	}

	return <-req.resp, <-req.err
}

func (b *chainscanFilteredChainView) rescanBlock(bh *chainhash.Hash) ([]*wire.MsgTx, int32, error) {
	header, err := b.chainSource.GetBlockHeader(b.ctx, bh)
	if err != nil {
		return nil, 0, err
	}

	bh, cfkey, filter, err := b.chainSource.GetCFilter(b.ctx, int32(header.Height))
	if err != nil {
		return nil, 0, err
	}

	e := chainscan.BlockConnectedEvent{
		Height:   int32(header.Height),
		Hash:     *bh,
		CFKey:    cfkey,
		Filter:   filter,
		PrevHash: header.PrevBlock,
	}

	if err := b.tipWatcher.ForceRescan(b.ctx, &e); err != nil {
		return nil, 0, err
	}

	return b.filterBlock(bh), int32(header.Height), nil
}

func (b *chainscanFilteredChainView) forceRescan(updateHeight, bestHeight int64) error {
	// Track the previous block hash to fill in the data.
	prevHash, err := b.chainSource.GetBlockHash(b.ctx, int32(updateHeight))
	if err != nil {
		return err
	}

	for height := updateHeight + 1; height < bestHeight+1; height++ {
		bh, cfkey, filter, err := b.chainSource.GetCFilter(b.ctx, int32(height))
		if err != nil {
			return err
		}

		e := chainscan.BlockConnectedEvent{
			Height:   int32(height),
			Hash:     *bh,
			CFKey:    cfkey,
			Filter:   filter,
			PrevHash: *prevHash,
		}
		prevHash = bh

		if err := b.tipWatcher.ForceRescan(b.ctx, &e); err != nil {
			log.Warnf("Unable to rescan block "+
				"with hash %s at height %d: %v",
				bh, height, err)
			continue
		}

		b.onBlockConnected(e)
	}

	return nil
}

// chainFilterer is the primary goroutine which: listens for new blocks coming
// and dispatches the relevant FilteredBlock notifications, updates the filter
// due to requests by callers, and finally is able to preform targeted block
// filtration.
//
// TODO(roasbeef): change to use loadfilter RPC's
func (b *chainscanFilteredChainView) chainFilterer() {
	defer b.wg.Done()

	for {
		select {
		// The caller has just sent an update to the current chain
		// filter, so we'll apply the update, possibly rewinding our
		// state partially.
		case update := <-b.filterUpdates:

			// First, we'll add all the new UTXO's to the set of
			// watched UTXO's, eliminating any duplicates in the
			// process.
			log.Tracef("Updating chain filter with new UTXO's: %v",
				newLogClosure(func() string {
					var s string
					for _, u := range update.newUtxos {
						s = s + u.OutPoint.String() + " "
					}
					return s
				}),
			)

			// All blocks gotten after we loaded the filter will
			// have the filter applied, but we will need to rescan
			// the blocks up to the height of the block we last
			// added to the blockQueue.
			b.bestHeightMtx.Lock()
			bestHeight := b.bestHeight
			b.bestHeightMtx.Unlock()

			cancelChans := make(map[wire.OutPoint]chan struct{}, len(update.newUtxos))
			b.filterMtx.Lock()
			for _, newOp := range update.newUtxos {
				// Ignore if we already watch this.
				if _, ok := b.chainFilter[newOp.OutPoint]; ok {
					continue
				}

				cancelChan := make(chan struct{})
				b.chainFilter[newOp.OutPoint] = cancelChan
				cancelChans[newOp.OutPoint] = cancelChan
			}
			b.filterMtx.Unlock()

			// Add the new utxos as targets for out instance of the
			// tip watcher.
			for _, newOp := range update.newUtxos {
				cancelChan := cancelChans[newOp.OutPoint]
				scriptVersion := uint16(0)
				swhChan := make(chan int32)
				target := chainscan.SpentOutPoint(
					newOp.OutPoint,
					scriptVersion,
					newOp.FundingPkScript,
				)
				b.tipWatcher.Find(
					target,
					chainscan.WithFoundCallback(b.foundAtTip),
					chainscan.WithCancelChan(cancelChan),
					chainscan.WithStartWatchHeightChan(swhChan),
				)

				// Wait until we know the tipWatcher has
				// started watching.
				select {
				case <-swhChan:
				case <-b.quit:
					return
				}
			}

			// If the update height matches our best known height,
			// then we don't need to do any rewinding.
			if update.updateHeight == bestHeight {
				continue
			}

			// Otherwise, we'll rewind the state to ensure the
			// caller doesn't miss any relevant notifications.
			err := b.forceRescan(update.updateHeight, bestHeight)
			if err != nil {
				log.Errorf("Error forcing utxo rescan on range (%d,%d]: %v",
					update.updateHeight, bestHeight, err)
			}

		// We've received a new request to manually filter a block.
		case req := <-b.filterBlockReqs:
			txs, height, err := b.rescanBlock(req.blockHash)
			if err != nil {
				req.err <- err
				req.resp <- nil
				continue
			}

			// Once we have this info, we can directly filter the
			// block and dispatch the proper notification.
			req.resp <- &FilteredBlock{
				Hash:         *req.blockHash,
				Height:       int64(height),
				Transactions: txs,
			}
			req.err <- err

		case <-b.quit:
			return
		}
	}
}

// csFilterUpdate is a message sent to the chainFilterer to update the current
// chainFilter state.
type csFilterUpdate struct {
	newUtxos     []channeldb.EdgePoint
	updateHeight int64
}

// UpdateFilter updates the UTXO filter which is to be consulted when creating
// FilteredBlocks to be sent to subscribed clients. This method is cumulative
// meaning repeated calls to this method should _expand_ the size of the UTXO
// sub-set currently being watched.  If the set updateHeight is _lower_ than
// the best known height of the implementation, then the state should be
// rewound to ensure all relevant notifications are dispatched.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) UpdateFilter(ops []channeldb.EdgePoint,
	updateHeight int64) error {

	// Make a copy to avoid having this changed under our feet.
	newUtxos := make([]channeldb.EdgePoint, len(ops))
	copy(newUtxos, ops)

	select {

	case b.filterUpdates <- csFilterUpdate{
		newUtxos:     newUtxos,
		updateHeight: updateHeight,
	}:
		return nil

	case <-b.quit:
		return fmt.Errorf("chain filter shutting down")
	}
}

// FilteredBlocks returns the channel that filtered blocks are to be sent over.
// Each time a block is connected to the end of a main chain, and appropriate
// FilteredBlock which contains the transactions which mutate our watched UTXO
// set is to be returned.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) FilteredBlocks() <-chan *FilteredBlock {
	return b.blockQueue.newBlocks
}

// DisconnectedBlocks returns a receive only channel which will be sent upon
// with the empty filtered blocks of blocks which are disconnected from the
// main chain in the case of a re-org.
//
// NOTE: This is part of the FilteredChainView interface.
func (b *chainscanFilteredChainView) DisconnectedBlocks() <-chan *FilteredBlock {
	return b.blockQueue.staleBlocks
}
