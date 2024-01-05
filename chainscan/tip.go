package chainscan

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/wire"
)

type ChainEvent interface {
	BlockHash() *chainhash.Hash
	BlockHeight() int32
	PrevBlockHash() *chainhash.Hash
	BlockHeader() *wire.BlockHeader

	nop()
}

type BlockConnectedEvent struct {
	Hash     chainhash.Hash
	Height   int32
	PrevHash chainhash.Hash
	CFKey    [16]byte
	Filter   *gcs.FilterV2
	Header   *wire.BlockHeader
}

func (e BlockConnectedEvent) BlockHash() *chainhash.Hash     { return &e.Hash }
func (e BlockConnectedEvent) BlockHeight() int32             { return e.Height }
func (e BlockConnectedEvent) PrevBlockHash() *chainhash.Hash { return &e.PrevHash }
func (e BlockConnectedEvent) BlockHeader() *wire.BlockHeader { return e.Header }
func (e BlockConnectedEvent) nop()                           {}
func (e BlockConnectedEvent) blockCF() *blockCFilter {
	return &blockCFilter{
		hash:       &e.Hash,
		height:     e.Height,
		cfilterKey: e.CFKey,
		cfilter:    e.Filter,
	}
}

type BlockDisconnectedEvent struct {
	Hash     chainhash.Hash
	Height   int32
	PrevHash chainhash.Hash
	Header   *wire.BlockHeader
}

func (e BlockDisconnectedEvent) BlockHash() *chainhash.Hash     { return &e.Hash }
func (e BlockDisconnectedEvent) BlockHeight() int32             { return e.Height }
func (e BlockDisconnectedEvent) BlockHeader() *wire.BlockHeader { return e.Header }
func (e BlockDisconnectedEvent) PrevBlockHash() *chainhash.Hash { return &e.PrevHash }
func (e BlockDisconnectedEvent) nop()                           {}

// TipChainSource defines the required backend functions for the TipWatcher
// scanner to perform its duties.
type TipChainSource interface {
	ChainSource

	// ChainEvents MUST return a channel that is sent ChainEvent's until
	// the passed context is canceled.
	ChainEvents(context.Context) <-chan ChainEvent
}

type eventReader struct {
	ctx context.Context
	c   chan ChainEvent
}

type forcedRescan struct {
	e    BlockConnectedEvent
	done chan error
}

type TipWatcher struct {
	ctx     context.Context
	running int32 // CAS 1=already running

	newTargetsChan chan []*target

	chain TipChainSource

	mtx          sync.Mutex
	eventReaders []*eventReader

	forcedRescanChan chan forcedRescan

	// The following fields are only used during testing.

	// If specified, after processing a new tip it will be signalled with
	// the new tip.
	tipProcessed chan *blockCFilter

	// If specified, Find() blocks until the target has been received by
	// the Run() goroutine.
	syncFind bool
}

func NewTipWatcher(chain TipChainSource) *TipWatcher {
	return &TipWatcher{
		chain:            chain,
		newTargetsChan:   make(chan []*target),
		forcedRescanChan: make(chan forcedRescan),
	}
}

// ChainEvents follows the same semantics as the TipChainSource ChainEvents but
// ensures all channels are only signalled _after_ the tip watcher has
// processed them.
func (tw *TipWatcher) ChainEvents(ctx context.Context) <-chan ChainEvent {
	r := &eventReader{
		ctx: ctx,
		c:   make(chan ChainEvent),
	}
	tw.mtx.Lock()
	tw.eventReaders = append(tw.eventReaders, r)
	tw.mtx.Unlock()
	return r.c
}

func (tw *TipWatcher) signalEventReaders(e ChainEvent) {
	tw.mtx.Lock()
	readers := tw.eventReaders
	tw.mtx.Unlock()

	for _, er := range readers {
		select {
		case <-er.ctx.Done():
		case er.c <- e:
		}
	}
}

// targetsForTipWatching splits a list of targets into three sublists assuming
// the current tip height of 'height':
//
//   - List of new targets that need to be watched.
//   - List of waiting targets which have not yet reached their watching height.
//   - List of stale targets for which their end height was reached.
func targetsForTipWatching(height int32, targets []*target) ([]*target, []*target, []*target) {
	var newTargets, staleTargets, waitingTargets []*target

	for _, t := range targets {
		switch {
		// A canceled target can't be watched for.
		case t.canceled():
			log.Tracef("Canceled target for tip watching: %s", t)

		// endHeight for watching this target already passed, so this
		// is actually a stale target.
		case t.endHeight != 0 && t.endHeight <= height:
			staleTargets = append(staleTargets, t)
			log.Tracef("Stale target for tip watching: %s", t)

		// New target to be watched.
		case t.startHeight <= height:
			newTargets = append(newTargets, t)
			log.Tracef("New target for tip watching: %s", t)

		// Haven't reached the height to start watching this target
		// yet.
		default:
			waitingTargets = append(waitingTargets, t)
			log.Tracef("Waiting target tip for watching: %s", t)
		}
	}

	return newTargets, staleTargets, waitingTargets
}

func (tw *TipWatcher) Run(ctx context.Context) error {
	if !atomic.CompareAndSwapInt32(&tw.running, 0, 1) {
		return errors.New("already running")
	}

	tw.ctx = ctx
	defer func() {
		tw.ctx = nil
		atomic.StoreInt32(&tw.running, 0)
	}()

	_, tipHeight, err := tw.chain.CurrentTip(ctx)
	if err != nil {
		return err
	}

	// Setup the chan that receives chain events.
	ceCtx, cancelCe := context.WithCancel(context.Background())
	defer cancelCe()
	chainEvents := tw.chain.ChainEvents(ceCtx)

	var waitingTargets []*target
	targets := newTargetList(nil)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case newTargets := <-tw.newTargetsChan:
			waitingTargets = append(waitingTargets, newTargets...)

		case fr := <-tw.forcedRescanChan:
			log.Debugf("Forcing rescan of (height=%d hash=%s)",
				fr.e.Height, fr.e.Hash)

			// We ignore errors here because we may have been
			// provided a wrong block hash (for example). A
			// canceled tw.ctx will be looked for in the next
			// iteration of the loop.
			fr.done <- scan(tw.ctx, fr.e.blockCF(), targets, tw.chain.GetBlock)

		case ce := <-chainEvents:
			// Ignore block disconnections.
			e, ok := ce.(BlockConnectedEvent)
			if !ok {
				log.Tracef("TipWatcher ignoring disconnect (height=%d hash=%s)",
					ce.BlockHeight(), ce.BlockHash())
				tw.signalEventReaders(ce)
				continue
			}

			log.Debugf("TipWatcher next tip received (height=%d hash=%s)",
				e.Height, e.Hash)
			err := scan(tw.ctx, e.blockCF(), targets, tw.chain.GetBlock)
			if err != nil {
				return err
			}
			tipHeight = e.Height

			stale := targets.removeStale(tipHeight)
			signalComplete(stale)

			tw.signalEventReaders(ce)
			if tw.tipProcessed != nil {
				tw.tipProcessed <- e.blockCF()
			}
		}

		// Update the list of watched targets with any new ones or ones
		// that might have been waiting for the startHeight to be
		// reached.
		brandNew, waiting, stale := targetsForTipWatching(tipHeight, waitingTargets)
		signalComplete(stale)
		waitingTargets = waiting
		targets.add(brandNew...)
		signalStartWatchHeight(brandNew, tipHeight)

		if targets.dirty {
			targets.rebuildCfilterEntries()
		}
	}
}

func (tw *TipWatcher) ForceRescan(ctx context.Context, e *BlockConnectedEvent) error {
	fr := forcedRescan{
		e:    *e,
		done: make(chan error),
	}
	select {
	case tw.forcedRescanChan <- fr:
	case <-ctx.Done():
		return ctx.Err()
	}

	return <-fr.done
}

func (tw *TipWatcher) Find(tgt Target, opts ...Option) error {
	t, ok := tgt.(*target)
	if !ok {
		return errors.New("provided target should be chainscan.*target")
	}

	for _, opt := range opts {
		opt(t)
	}

	switch {
	case t.endHeight < 0:
		return errors.New("cannot tip watch with endHeight < 0")
	case t.endHeight == 0:
		// Maximum endHeight so the target never goes stale.
		t.endHeight = 1<<31 - 1
	}

	// syncFind should only be specified during tests since it risks
	// deadlocking the tipWatcher.
	if tw.syncFind {
		tw.newTargetsChan <- []*target{t}
		return nil
	}

	go func() {
		tw.newTargetsChan <- []*target{t}
	}()

	return nil
}
