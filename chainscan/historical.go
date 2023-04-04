package chainscan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v3"
)

type HistoricalChainSource interface {
	ChainSource

	// GetCFilter MUST return the block hash, key and filter for the given
	// mainchain block height.
	//
	// If a height higher than the current mainchain tip is specified, the
	// chain source MUST return ErrBlockAfterTip so that the historical
	// scanner will correctly handle targets specified with an invalid
	// ending height.
	GetCFilter(context.Context, int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error)
}

type Historical struct {
	mtx sync.Mutex
	ctx context.Context

	newTargetsChan chan []*target
	newTargetCount int64

	// nextBatchTargets are the targets which startHeight have already
	// passed the current height of the batch and will need to be included
	// in the next batch.
	nextBatchTargets []*target

	chain HistoricalChainSource
}

func NewHistorical(chain HistoricalChainSource) *Historical {
	return &Historical{
		chain:          chain,
		newTargetsChan: make(chan []*target),
	}
}

// nextBatchRun returns the next run for the given batch of targets.  The batch
// is modified so that targets left out of this run remain on it.
func nextBatchRun(batch *targetHeap) *targetHeap {
	if len(*batch) == 0 {
		return &targetHeap{}
	}

	// Begin a new run.
	run := &targetHeap{batch.pop()}
	startHeight := run.peak().startHeight

	// Find all items that have the same startHeight.
	for t := batch.peak(); t != nil && t.startHeight == startHeight; t = batch.peak() {
		run.push(batch.pop())
	}
	return run
}

func targetsForNextBatch(height int32, newTargets []*target) ([]*target, []*target) {
	var thisBatch, nextBatch []*target
	for _, nt := range newTargets {
		switch {
		case nt.startHeight <= height:
			log.Tracef("New target delayed to next batch due to %d <= %d",
				nt.startHeight, height)
			nextBatch = append(nextBatch, nt)

		default:
			log.Tracef("New target Added to this batch due to %d > %d",
				nt.startHeight, height)
			thisBatch = append(thisBatch, nt)
		}
	}

	return thisBatch, nextBatch
}

func (h *Historical) drainNewTargets(waiting []*target) ([]*target, error) {
	var zeroEndHeight bool

	// Determine if any of the outstanding waiting targets has zero
	// endHeight.
	for _, t := range waiting {
		if t.endHeight > 0 {
			continue
		}
		zeroEndHeight = true
		break
	}

	// While there are outstanding new targets to be received, block while
	// waiting for them so we can decide what to do (add to the current
	// batch or keep it until the next batch starts).
	for atomic.LoadInt64(&h.newTargetCount) > 0 {
		select {
		case <-h.ctx.Done():
			return nil, h.ctx.Err()
		case newTargets := <-h.newTargetsChan:
			waiting = append(waiting, newTargets...)
			atomic.AddInt64(&h.newTargetCount, -1)

			// If we haven't determined there are targets with an
			// endHeight with a zero value, verify this new batch.
			if zeroEndHeight {
				continue
			}
			for _, nt := range newTargets {
				if nt.endHeight <= 0 {
					zeroEndHeight = true
					break
				}
			}

		}
	}

	// If there are new targets with a zero endHeight, fetch the current
	// tip and fill their endHeight.
	if zeroEndHeight {
		_, endHeight, err := h.chain.CurrentTip(h.ctx)
		if err != nil {
			return nil, err
		}

		for _, nt := range waiting {
			if nt.endHeight <= 0 {
				nt.endHeight = endHeight
			}
		}
	}

	return waiting, nil
}

// rescanRun performs the rescan across a single "run" of targets in ascending
// block height.
func (h *Historical) rescanRun(targets *targetList, batch *targetHeap, startHeight int32) error {
	var (
		bcf blockCFilter
		err error
	)

	log.Tracef("Starting run with %d targets at height %d", len(targets.targets),
		startHeight)

	for bcf.height = startHeight; !targets.empty(); bcf.height++ {
		if h.ctxDone() {
			return h.ctx.Err()
		}

		// Fetch cfilter for this block & process it.
		bcf.hash, bcf.cfilterKey, bcf.cfilter, err = h.chain.GetCFilter(h.ctx, bcf.height)
		if err != nil {
			if errors.Is(err, ErrBlockAfterTip{}) {
				// This means at least one target was specified
				// with an endHeight past the current tip or
				// we're in the middle of a reorg. In any case,
				// this isn't a critical error as specified in
				// the documentation for this scanner.
				//
				// Signal all targets as complete.
				signalComplete(targets.removeAll())
				return nil
			}
			return err
		}

		err = scan(h.ctx, &bcf, targets, h.chain.GetBlock)
		if err != nil {
			return err
		}

		// Fetch and split new targets between those that can be
		// processed in this batch and those that will need to wait for
		// the next batch.
		newTargets, err := h.drainNewTargets(targets.addedDuringScan)
		if err != nil {
			return err
		}
		thisBatch, nextBatch := targetsForNextBatch(bcf.height, newTargets)
		batch.push(thisBatch...)
		h.nextBatchTargets = append(h.nextBatchTargets, nextBatch...)

		// Add any targets that can extend this run.
		for t := batch.peak(); t != nil && t.startHeight == bcf.height+1; t = batch.peak() {
			targets.add(batch.pop())
			log.Tracef("Added target to run at height %d: %s",
				bcf.height, t)
		}

		// Remove canceled and targets which reached their endHeight
		// and signal their completion.
		stale := targets.removeStale(bcf.height)
		signalComplete(stale)

		if targets.dirty {
			targets.rebuildCfilterEntries()
		}
	}

	log.Tracef("Ended run at height %d", bcf.height-1)
	return nil
}

// rescanBatch performs a rescan across all outstanding targets (a "batch" of
// targets) in ascending block height order.
//
// A batch may be composed of multiple disjoint "runs".
func (h *Historical) rescanBatch(targets []*target) error {
	batch := asTargetHeap(targets)

	log.Debugf("Starting batch of %d targets", len(targets))

	tl := newTargetList(nil)
	for batch.peak() != nil {
		run := nextBatchRun(batch)
		startHeight := run.peak().startHeight
		tl.add(*run...)
		tl.rebuildCfilterEntries()
		err := h.rescanRun(tl, batch, startHeight)
		if err != nil {
			return err
		}
	}

	return nil
}

func (h *Historical) Run(ctx context.Context) error {
	h.mtx.Lock()
	if h.ctx != nil {
		h.mtx.Unlock()
		return errors.New("already running")
	}
	h.ctx = ctx
	h.mtx.Unlock()

	var err error

	for {
		// Process any existing waiting targets or wait for some to
		// arrive.
		newTargets := h.nextBatchTargets
		h.nextBatchTargets = nil
		if len(newTargets) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case newTargets = <-h.newTargetsChan:
				atomic.AddInt64(&h.newTargetCount, -1)
			}
		}
		newTargets, err = h.drainNewTargets(newTargets)
		if err != nil {
			return err
		}

		err = h.rescanBatch(newTargets)
		if err != nil {
			return err
		}
	}
}

// applyOptions applies the given options to the given target and returns the
// concrete implementation of the target or an error.
//
// This is used to ensure the same validation rules are used in both Find and
// FindMany.
func (h *Historical) applyOptions(tgt Target, opts []Option) (*target, error) {
	t, ok := tgt.(*target)
	if !ok {
		return nil, errors.New("provided target should be chainscan.*target")
	}

	for _, opt := range opts {
		opt(t)
	}

	if t.endHeight > 0 && t.endHeight < t.startHeight {
		return nil, errors.New("malformed query (endHeight < startHeight)")
	}

	return t, nil
}

// ctxDone returns true when the historical's ctx is both filled and Done().
func (h *Historical) ctxDone() bool {
	h.mtx.Lock()
	ctx := h.ctx
	h.mtx.Unlock()
	if ctx == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (h *Historical) Find(tgt Target, opts ...Option) error {
	if h.ctxDone() {
		return errors.New("historical scanner finished running")
	}

	t, err := h.applyOptions(tgt, opts)
	if err != nil {
		return err
	}

	// We're about to add new targets, so setup the flag that will let the
	// batch processor know to wait for them.
	newCount := atomic.AddInt64(&h.newTargetCount, 1)
	if newCount < 0 {
		// How did we wrap around an int64? This is super bad.
		panic(fmt.Errorf("wrap around newTargetCount"))
	}

	// Signal the existance of new targets in a new goroutine to avoid
	// locking.
	go func() {
		h.newTargetsChan <- []*target{t}
	}()

	return nil
}

// FindMany attempts to search for many targets at once. This is better than
// making individual calls to Find() when they should be searched for at the
// same starting height since all specified targets are guaranteed to be
// included in the same search batch.
//
// This function is safe for concurrent calls in multiple goroutines, including
// inside functions specified with WithFoundCallback() options.
func (h *Historical) FindMany(targets []TargetAndOptions) error {
	if h.ctxDone() {
		return errors.New("historical scanner finished running")
	}

	ts := make([]*target, len(targets))
	var err error

	for i, tgt := range targets {
		ts[i], err = h.applyOptions(tgt.Target, tgt.Options)
		if err != nil {
			return err
		}
	}

	// We're about to add new targets, so setup the flag that will let the
	// batch processor know to wait for them.
	newCount := atomic.AddInt64(&h.newTargetCount, 1)
	if newCount < 0 {
		// How did we wrap around an int64? This is super bad.
		panic(fmt.Errorf("wrap around newTargetCount"))
	}

	// Signal the existance of new targets in a new goroutine to avoid
	// locking.
	go func() {
		h.newTargetsChan <- ts
	}()

	return nil

}
