package chainscan

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v3"
	"github.com/decred/dcrd/wire"
	"github.com/decred/slog"
)

type MatchField uint8

const (
	MatchTxIn MatchField = 1 << iota
	MatchTxOut
)

func (m MatchField) String() string {
	switch m {
	case MatchTxIn:
		return "in"
	case MatchTxOut:
		return "out"
	case MatchTxIn | MatchTxOut:
		return "in|out"
	default:
		return "invalid"
	}
}

var zeroOutPoint = wire.OutPoint{}

type ChainSource interface {
	GetBlock(context.Context, *chainhash.Hash) (*wire.MsgBlock, error)
	CurrentTip(context.Context) (*chainhash.Hash, int32, error)
}

// ErrBlockAfterTip should be returned by chains when the requested block is
// after the currently known tip. Scanners may choose not to fail their run if
// this specific error is returned.
type ErrBlockAfterTip struct {
	Height int32
}

func (e ErrBlockAfterTip) Error() string {
	return fmt.Sprintf("block %d is after currently known tip", e.Height)
}

func (e ErrBlockAfterTip) Is(err error) bool {
	_, ok := err.(ErrBlockAfterTip)
	return ok
}

type Event struct {
	BlockHeight  int32
	BlockHash    chainhash.Hash
	TxIndex      int32
	Tx           *wire.MsgTx
	Index        int32
	Tree         int8
	MatchedField MatchField
}

func (e Event) String() string {
	if (e.Tx) == nil {
		return "unfilled event"
	}

	switch e.MatchedField {
	case MatchTxOut:
		return fmt.Sprintf("txOut=%s:%d height=%d",
			e.Tx.CachedTxHash(), e.Index, e.BlockHeight)
	case MatchTxIn:
		return fmt.Sprintf("txIn=%s:%d height=%d",
			e.Tx.CachedTxHash(), e.Index, e.BlockHeight)
	default:
		return "unknown matched field"
	}
}

type FindFunc func(Target, ...Option) error
type FoundCallback func(Event, FindFunc)

// Target represents the desired target (outpoint, pkscript, etc) of a search.
//
// NOTE: targets are *not* safe for reuse for multiple searches.
type Target interface {
	// nop is meant to prevent external packages implementing this
	// interface.
	nop()
}

const (
	flagMatchTxIn = 1 << iota
	flagMatchTxOut
	flagMatchOutpoint
	flagMatchTxHash
)

type target struct {
	// Fields that specify _what_ to match against

	flags    int
	out      wire.OutPoint
	version  uint16
	pkscript []byte

	// Fields that control the _behavior_ during scans.

	startHeight          int32
	endHeight            int32
	foundCb              FoundCallback
	foundChan            chan<- Event
	completeChan         chan struct{}
	startWatchHeightChan chan int32
	cancelChan           <-chan struct{}
}

func (c *target) String() string {
	match := "unknown"
	switch c.flags {
	case flagMatchTxIn | flagMatchOutpoint:
		match = "TxIn(outp)"
	case flagMatchTxIn:
		match = "TxIn(script)"
	case flagMatchTxOut | flagMatchOutpoint:
		match = "TxOut(outp)"
	case flagMatchTxOut | flagMatchTxHash:
		match = "TxOut(tx)"
	case flagMatchTxOut:
		match = "TxOut(script)"
	}

	return fmt.Sprintf("id=%p match=%s pkscript=%x out=%s", c, match,
		c.pkscript, c.out)
}

func (c *target) canceled() bool {
	select {
	case <-c.cancelChan:
		return true
	default:
		return false
	}
}

// nop fulfills the Target interface.
func (c *target) nop() {}

// ConfirmedOutPoint tries to match against a TxOut that spends the provided
// script but only as long as the output itself fulfills the specified outpoint
// (that is, the output is created in the transaction specified by out.Hash and
// at the out.Index position).
func ConfirmedOutPoint(out wire.OutPoint, version uint16, pkscript []byte) Target {
	return &target{
		flags:    flagMatchTxOut | flagMatchOutpoint,
		out:      out,
		version:  version,
		pkscript: pkscript,
	}
}

func ConfirmedTransaction(txh chainhash.Hash, version uint16, pkscript []byte) Target {
	return &target{
		flags: flagMatchTxOut | flagMatchTxHash,
		out: wire.OutPoint{
			Hash: txh,
		},
		version:  version,
		pkscript: pkscript,
	}
}

func ConfirmedScript(version uint16, pkscript []byte) Target {
	return &target{
		flags:    flagMatchTxOut,
		version:  version,
		pkscript: pkscript,
	}
}

// SpentScript tries to match against a TxIn that spends the provided script.
//
// NOTE: only version 0 scripts are currently supported. Also, the match is a
// best effort one, based on the "shape" of the signature script of the txin.
// See ComputePkScript for details on supported script types.
func SpentScript(version uint16, pkscript []byte) Target {
	return &target{
		flags:    flagMatchTxIn,
		version:  version,
		pkscript: pkscript,
	}
}

// SpentOutPoint tries to match against a TxIn that spends the given outpoint
// and pkscript combination.
//
// NOTE: If the provided pkscript does _not_ actually correspond to the
// outpoint, the search may never trigger a match.
func SpentOutPoint(out wire.OutPoint, version uint16, pkscript []byte) Target {
	return &target{
		flags:    flagMatchTxIn | flagMatchOutpoint,
		out:      out,
		version:  version,
		pkscript: pkscript,
	}
}

type Option func(*target)

// WithStartHeight allows a caller to specify a block height after which a scan
// should trigger events when this target is found.
func WithStartHeight(startHeight int32) Option {
	return func(t *target) {
		t.startHeight = startHeight
	}
}

// WithEndHeight allows a caller to specify a block height after which scans
// should no longer happen.
func WithEndHeight(endHeight int32) Option {
	return func(t *target) {
		t.endHeight = endHeight
	}
}

// WithFoundCallback allows a caller to specify a callback that is called
// synchronously in relation to a scanner when the related target is found in a
// block.
//
// This callback is called in the same goroutine that performs scans, therefore
// it is *NOT* safe to perform any receives or sends on channels that also
// affect this or other target's searches (such as waiting for a
// StartWatchingHeight, FoundChan or other signals).
//
// It is also generally not advised to perform lengthy operations inside this
// callback since it blocks all other searches from progressing.
//
// This callback is intended to be used in situations where the caller needs to
// add new targets to search for as a result of having found a match within a
// block.
//
// There are two ways to add addicional targets during the execution of the
// foundCallback:
//
//  1. Via additional Find() calls of the scanner (which _are_ safe for direct
//     calling by the foundCallback). This allows callers to start to search for
//     additional targets on the _next_ block checked by the scanner.
//
//  2. Via the second argument to the foundCallback. That function can add
//     targets to search for, starting at the _current_ block, transaction list
//     and transaction index. This is useful (for example) when detecting a
//     specific script was used in an output should trigger a search for other
//     scripts including in the same block.
//
// The callback function may be called multiple times for a given target.
func WithFoundCallback(cb FoundCallback) Option {
	return func(t *target) {
		t.foundCb = cb
	}
}

// WithFoundChan allows a caller to specify a channel that receives events when
// a match for a given target is found. This event is called concurrently with
// the rest of the search process.
//
// Callers are responsible for draining this channel once the search completes,
// otherwise data may leak. They should also ensure the channel is _not_ closed
// until the search completes, otherwise the scanner may panic.
//
// The channel may be sent to multiple times.
func WithFoundChan(c chan<- Event) Option {
	return func(t *target) {
		t.foundChan = c
	}
}

// WithCompleteChan allows a caller to specify a channel that gets closed once
// the endHeight was reached for the target.
func WithCompleteChan(c chan struct{}) Option {
	return func(t *target) {
		t.completeChan = c
	}
}

// WithCancelChan allows a caller to specify a channel that, once closed,
// removes the provided target from being scanned for.
func WithCancelChan(c <-chan struct{}) Option {
	return func(t *target) {
		t.cancelChan = c
	}
}

// WithStartWatchHeightChan allows a caller to specify a channel that receives
// the block height after which events will be triggered if the target is
// found.
func WithStartWatchHeightChan(c chan int32) Option {
	return func(t *target) {
		t.startWatchHeightChan = c
	}
}

type TargetAndOptions struct {
	Target  Target
	Options []Option
}

const txOutKeyLen = 32

type txOutKey [txOutKeyLen]byte

func newTxOutKey(version uint16, pkscript []byte) txOutKey {
	// key := make([]byte, 2+len(pkscript))
	var key txOutKey
	key[0] = byte(version >> 8)
	key[1] = byte(version)
	copy(key[2:], pkscript)
	return key

}

// targetSlice is a slice of targets that can be modified in-place by adding
// and removing items via the add and del functions.
//
// This is used to simplify the code of some operations in scanners (vs using
// regular slices).
type targetSlice []*target

func (ts *targetSlice) del(t *target) {
	s := *ts
	for i := 0; i < len(s); i++ {
		if s[i] == t {
			s[i] = s[len(s)-1]
			s[len(s)-1] = nil
			*ts = s[:len(s)-1]
			return
		}
	}
}

func (ts *targetSlice) add(t *target) {
	s := *ts
	*ts = append(s, t)
}

func (ts *targetSlice) empty() bool {
	return len(*ts) == 0
}

// targetHeap is a sortable slice of targets that fulfills the heap.Interface
// interface by sorting in ascending order of start height.
type targetHeap []*target

func (th targetHeap) Len() int           { return len(th) }
func (th targetHeap) Less(i, j int) bool { return th[i].startHeight < th[j].startHeight }
func (th targetHeap) Swap(i, j int)      { th[i], th[j] = th[j], th[i] }

func (th *targetHeap) Push(x interface{}) {
	*th = append(*th, x.(*target))
}

func (th *targetHeap) Pop() interface{} {
	old := *th
	n := len(old)
	x := old[n-1]
	old[n-1] = nil // Avoid leaking the target.
	*th = old[0 : n-1]
	return x
}

func (th *targetHeap) push(ts ...*target) {
	for _, t := range ts {
		heap.Push(th, t)
	}
}

func (th *targetHeap) pop() *target {
	return heap.Pop(th).(*target)
}

func (th *targetHeap) peak() *target {
	if len(*th) > 0 {
		return (*th)[0]
	}
	return nil
}

// asTargetHeap returns the slice of targets as a targetHeap. The order of
// elements of the backing array of the given slice may be modified by this
// fuction.
func asTargetHeap(targets []*target) *targetHeap {
	th := targetHeap(targets)
	heap.Init(&th)
	return &th
}

var _ heap.Interface = (*targetHeap)(nil)

// targetList is a list of targets which can be queried for matches against
// blocks.
//
// It maintains several caches so that queries for the multiple targets in a
// block can be done only in linear time (on the number of inputs+outputs of
// the transactions).
//
// targetList values aren't safe for concurrent access from multiple
// goroutines.
type targetList struct {
	targets        map[*target]struct{}
	cfEntries      [][]byte
	dirty          bool
	txInKeys       map[wire.OutPoint]*targetSlice
	txInScriptKeys map[txOutKey]*targetSlice
	txOutKeys      map[txOutKey]*targetSlice

	// blockHeight is the current height being processed during a call for
	// singalFound().
	blockHeight int32

	// ff stores a reference to the the target list's addDuringFoundCb
	// function. Storing as a field here avoids having to perform an
	// allocation during the critical code path when matches occur in a
	// block.
	ff FindFunc
}

func newTargetList(initial []*target) *targetList {
	tl := &targetList{
		targets:        make(map[*target]struct{}, len(initial)),
		txInKeys:       make(map[wire.OutPoint]*targetSlice, len(initial)),
		txInScriptKeys: make(map[txOutKey]*targetSlice, len(initial)),
		txOutKeys:      make(map[txOutKey]*targetSlice, len(initial)),
	}

	for _, t := range initial {
		tl.add(t)
	}
	tl.rebuildCfilterEntries()

	tl.ff = FindFunc(tl.addDuringFoundCb)

	return tl
}

func (tl *targetList) empty() bool {
	return len(tl.targets) == 0
}

func (tl *targetList) add(newTargets ...*target) {
	for _, nt := range newTargets {
		if _, ok := tl.targets[nt]; ok {
			continue
		}

		tl.targets[nt] = struct{}{}
		if nt.flags&flagMatchTxIn == flagMatchTxIn {
			if nt.flags&flagMatchOutpoint == 0 {
				// Match by input script.
				key := newTxOutKey(nt.version, nt.pkscript)
				if _, ok := tl.txInScriptKeys[key]; !ok {
					tl.txInScriptKeys[key] = &targetSlice{}
				}
				tl.txInScriptKeys[key].add(nt)
			} else {
				// Match by outpoint.
				outp := nt.out
				if _, ok := tl.txInKeys[outp]; !ok {
					tl.txInKeys[outp] = &targetSlice{}
				}
				tl.txInKeys[outp].add(nt)
			}
		}

		if nt.flags&flagMatchTxOut == flagMatchTxOut {
			key := newTxOutKey(nt.version, nt.pkscript)
			if _, ok := tl.txOutKeys[key]; !ok {
				tl.txOutKeys[key] = &targetSlice{}
			}
			tl.txOutKeys[key].add(nt)
		}
	}

	tl.dirty = true
}

func (tl *targetList) removeAll() []*target {
	targets := make([]*target, len(tl.targets))
	var i int
	for t := range tl.targets {
		targets[i] = t
	}
	tl.remove(targets...)
	return targets
}

func (tl *targetList) remove(newTargets ...*target) {
	for _, nt := range newTargets {
		if _, ok := tl.targets[nt]; !ok {
			continue
		}

		tl.dirty = true

		if nt.flags&flagMatchTxIn == flagMatchTxIn {
			if nt.out == zeroOutPoint {
				key := newTxOutKey(nt.version, nt.pkscript)
				if _, ok := tl.txInScriptKeys[key]; ok {
					tl.txInScriptKeys[key].del(nt)
					if tl.txInScriptKeys[key].empty() {
						delete(tl.txInScriptKeys, key)
					}
				}
			} else {
				outp := nt.out
				if _, ok := tl.txInKeys[outp]; ok {
					tl.txInKeys[outp].del(nt)
					if tl.txInKeys[outp].empty() {
						delete(tl.txInKeys, outp)
					}
				}
			}
		}

		if nt.flags&flagMatchTxOut == flagMatchTxOut {
			key := newTxOutKey(nt.version, nt.pkscript)
			if _, ok := tl.txOutKeys[key]; ok {
				tl.txOutKeys[key].del(nt)
				if tl.txOutKeys[key].empty() {
					delete(tl.txOutKeys, key)
				}
			}
		}

		delete(tl.targets, nt)
	}
}

// removeStale removes all targets that have been canceled or for which their
// endHeight was reached as specified by the 'height' parameter. This function
// returns only the targets for which the endHeight was reached.
func (tl *targetList) removeStale(height int32) []*target {
	var stale []*target
	for t := range tl.targets {
		del := false
		if height >= t.endHeight {
			// Got to the end of the watching interval for the
			// given target.
			stale = append(stale, t)
			del = true
			log.Tracef("Removing stale target at %d: %s", height, t)
		}

		select {
		case <-t.cancelChan:
			del = true
			log.Tracef("Removing canceled target at %d: %s", height, t)
		default:
		}

		if del {
			tl.remove(t)
		}
	}

	return stale
}

func (tl *targetList) rebuildCfilterEntries() {
	cf := make([][]byte, 0, len(tl.targets))
	for t := range tl.targets {
		cf = append(cf, t.pkscript)
	}
	tl.cfEntries = cf
	tl.dirty = false
}

func (tl *targetList) addDuringFoundCb(tgt Target, opts ...Option) error {
	t, ok := tgt.(*target)
	if !ok {
		return errors.New("provided target should be chainscan.*target")
	}

	for _, opt := range opts {
		opt(t)
	}

	if t.endHeight > 0 && t.endHeight < tl.blockHeight {
		return errors.New("cannot add targets during foundCb with " +
			"endHeight lower than the current blockHeight")
	}

	if t.startHeight != 0 && t.startHeight != tl.blockHeight {
		return errors.New("cannot add targets during foundCb with " +
			"startHeight different than the current blockHeight")
	}

	tl.add(t)
	return nil
}

// signalMatches finds and signals all matches of the current target list in
// the given block.
func (tl *targetList) signalFound(blockHeight int32, blockHash *chainhash.Hash, block *wire.MsgBlock) {
	// Filled after the first match on a transaction is found.
	var txid chainhash.Hash
	var ptxid *chainhash.Hash

	var event Event

	tl.blockHeight = blockHeight

	// Whether there are any inputs that will be matched by script only
	// instead of by outpoint.
	matchByInScript := len(tl.txInScriptKeys) > 0

	// Flag to test against so we verify a match against a specific output
	// of a confirmed output.
	flagTxOutWithOutp := flagMatchTxOut | flagMatchOutpoint

	// Fill events for all targets from ts that have matched against the
	// transaction tx at field f and index i.
	fillMatches := func(ts []*target, tx *wire.MsgTx, tree int8, i int32, f MatchField, txi int32) {
		// "large" scripts are those that surpass the the maximum key
		// length size for output matching. In that case, we also need
		// to check the full script on targets.
		//
		// This should be a rare occurrence given the absolute majority
		// of scripts are P2PKH and P2SH.
		//
		// Scripts in inputs don't need this check because
		// ComputePkScript only supports P2PKH and P2SH.
		largeScript := f == MatchTxOut && len(tx.TxOut[i].PkScript) > txOutKeyLen

		for _, t := range ts {
			// Canceled targets can't be triggered.
			if t.canceled() {
				continue
			}

			// Large scripts that don't match aren't signalled.
			if largeScript && !bytes.Equal(t.pkscript, tx.TxOut[i].PkScript) {
				continue
			}

			if t.flags&flagTxOutWithOutp == flagTxOutWithOutp {
				// When matching against TxOut's, if the
				// outpoint in the target is specified we only
				// match against that specific output. So we
				// need to calculate the txid and verify the
				// target's outpoint with the txid+index.
				//
				// We check the index and tree first since
				// there's no point in calculating the txid if
				// the other fields don't match.
				if i != int32(t.out.Index) || tree != t.out.Tree {
					continue
				}

				// Calculate tx hash if needed.
				if ptxid == nil {
					txid = tx.TxHash()
					ptxid = &txid
				}

				if txid != t.out.Hash {
					continue
				}
			}

			// Match against only the tx hash.
			if t.flags&flagMatchTxHash == flagMatchTxHash {
				// Calculate tx hash if needed.
				if ptxid == nil {
					txid = tx.TxHash()
					ptxid = &txid
				}

				if txid != t.out.Hash {
					continue
				}
			}

			// Found a match!
			event = Event{
				BlockHeight:  blockHeight,
				BlockHash:    *blockHash,
				TxIndex:      txi,
				Tx:           tx,
				Index:        i,
				Tree:         tree,
				MatchedField: f,
			}

			if log.Level() <= slog.LevelDebug {
				log.Debugf("Matched %s for target %s", event, t)
			}

			if t.foundCb != nil {
				// foundCb() is called synchronously so that it
				// can modify scanners before the scan
				// continues.
				t.foundCb(event, tl.ff)
				matchByInScript = len(tl.txInScriptKeys) > 0
			}
			if t.foundChan != nil {
				// foundChan is signalled asynchronously so
				// scans aren't blocked.
				go func(c chan<- Event, e Event) {
					c <- e
				}(t.foundChan, event)
			}
		}
	}

	// Process both the regular and stake transaction trees.
	trees := []int8{wire.TxTreeRegular, wire.TxTreeStake}
	var txs []*wire.MsgTx

	for _, tree := range trees {
		switch tree {
		case wire.TxTreeStake:
			txs = block.STransactions
		default:
			txs = block.Transactions
		}

		for txi, tx := range txs {
			ptxid = nil
			for i, in := range tx.TxIn {
				if ts, ok := tl.txInKeys[in.PreviousOutPoint]; ok {
					fillMatches(*ts, tx, tree, int32(i), MatchTxIn, int32(txi))
				}

				// Only continue to process the input if we
				// need to match by input script.
				if !matchByInScript {
					continue
				}

				script, err := ComputePkScript(0, in.SignatureScript)
				if err != nil {
					// If this is an unrecognized script
					// type it can't possibly be a match.
					continue
				}

				// Guessing it's a version 0 script. How to
				// support other types?
				key := newTxOutKey(0, script.Script())
				if ts, ok := tl.txInScriptKeys[key]; ok {
					fillMatches(*ts, tx, tree, int32(i), MatchTxIn, int32(txi))
				}
			}

			for i, out := range tx.TxOut {
				key := newTxOutKey(out.Version, out.PkScript)
				if ts, ok := tl.txOutKeys[key]; ok {
					fillMatches(*ts, tx, tree, int32(i), MatchTxOut, int32(txi))
				}
			}
		}
	}
}

// signalComplete closes the completeChan of all targets in the specified
// slice.
func signalComplete(targets []*target) {
	for _, t := range targets {
		if t.completeChan != nil {
			close(t.completeChan)
		}
	}
}

// signalStartWatchHeight sends the given height as the start watching height
// for all applicable targets in the slice.
func signalStartWatchHeight(targets []*target, height int32) {
	for _, t := range targets {
		if t.startWatchHeightChan != nil {
			go func(c chan int32) {
				c <- height
			}(t.startWatchHeightChan)
		}
	}
}

// blockCFilter is an auxillary structure used to hold all data required to
// query a v2 cfilter of a given block.
type blockCFilter struct {
	hash       *chainhash.Hash
	height     int32
	cfilterKey [16]byte
	cfilter    *gcs.FilterV2
}

func (bcf blockCFilter) matches(entries [][]byte) bool {
	return bcf.cfilter.MatchAny(bcf.cfilterKey, entries)
}

// scan performs a cfilter and then (if needed) a full block scan in the given
// blockcf for the specified target list.
//
// getBlock must be able to fetch the specified full block data.
//
// Note that the `targets` target list may have been modified by this call,
// therefore callers should check whether the target list is dirty and rebuild
// cfilter entries as appropriate.
func scan(ctx context.Context, blockcf *blockCFilter, targets *targetList, getBlock func(context.Context, *chainhash.Hash) (*wire.MsgBlock, error)) error {
	if !blockcf.matches(targets.cfEntries) {
		return nil
	}

	// Find and process matches in the actual block, given the cfilter test
	// passed.
	block, err := getBlock(ctx, blockcf.hash)
	if err != nil {
		return err
	}

	// Alert clients of matches found.
	targets.signalFound(blockcf.height, blockcf.hash, block)

	return nil
}
