package chainscan

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/wire"
)

type histTestCtx struct {
	chain  *mockChain
	hist   *Historical
	cancel func()
	t      testingIntf
}

func newHistTestCtx(t testingIntf) *histTestCtx {
	ctx, cancel := context.WithCancel(context.Background())
	chain := newMockChain()
	hist := NewHistorical(chain)
	chain.extend(chain.newFromTip()) // Genesis block

	go func() {
		hist.Run(ctx)
	}()

	return &histTestCtx{
		chain:  chain,
		hist:   hist,
		cancel: cancel,
		t:      t,
	}
}

func (h *histTestCtx) cleanup() {
	h.cancel()
}

func (h *histTestCtx) genBlocks(n int, allCfiltersMatch bool) {
	h.t.Helper()
	var manglers []blockMangler

	if allCfiltersMatch {
		manglers = append(manglers, cfilterData(testPkScript))
	}

	h.chain.genBlocks(n, manglers...)
}

// TestHistorical tests the basic behavior of the historical scanner against
// the scannerTestCases, which must be fulfilled by both the historical and tip
// watcher scanners.
func TestHistorical(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		var foundCbEvent, foundChanEvent Event
		completeChan := make(chan struct{})
		foundChan := make(chan Event)
		tc := newHistTestCtx(t)
		defer tc.cleanup()

		// The generated test chain is:
		// - 5 blocks that miss the cfilter match
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		// - 5 blocks with a cfilter match
		tc.genBlocks(5, false)
		tc.genBlocks(5, true)
		b := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b)
		tc.genBlocks(5, true)

		assertNoError(t, tc.hist.Find(
			c.target(b),
			WithFoundCallback(func(e Event, _ FindFunc) { foundCbEvent = e }),
			WithFoundChan(foundChan),
			WithCompleteChan(completeChan),
		))

		// Wait until the search is complete.
		assertCompleted(tc.t, completeChan)

		if !c.wantFound {
			// Testing when we don't expect a match.

			assertFoundChanEmpty(tc.t, foundChan)
			if foundCbEvent != emptyEvent {
				t.Fatalf("unexpected foundCallback triggered with %s", &foundCbEvent)
			}

			// Nothing else to test since we didn't expect a match.
			return
		}

		// Testing when we expect a match.

		select {
		case foundChanEvent = <-foundChan:
		case <-time.After(5 * time.Second):
			t.Fatal("found chan not triggered in time")
		}

		if foundCbEvent == emptyEvent {
			t.Fatal("foundCallback not triggered")
		}

		if foundChanEvent != foundCbEvent {
			t.Fatal("cb and chan showed different events")
		}

		e := foundChanEvent
		if e.MatchedField != c.wantMF {
			t.Fatalf("unexpected matched field. want=%s got=%s",
				c.wantMF, e.MatchedField)
		}

		if e.BlockHeight != int32(b.block.Header.Height) {
			t.Fatalf("unexpected matched block height. want=%d got=%d",
				b.block.Header.Height, e.BlockHeight)
		}

		if e.BlockHash != b.block.Header.BlockHash() {
			t.Fatalf("unexpected matched block hash. want=%s got=%s",
				b.block.Header.BlockHash(), e.BlockHash)
		}

		// All tests always match against the first transaction in the
		// block in either the stake or regular tx tree.
		var tx *wire.MsgTx
		var tree int8
		if len(b.block.Transactions) > 0 {
			tx = b.block.Transactions[0]
			tree = wire.TxTreeRegular
		} else {
			tx = b.block.STransactions[0]
			tree = wire.TxTreeStake
		}
		if e.Tx.TxHash() != tx.TxHash() {
			t.Fatalf("unexpected tx match. want=%s got=%s",
				b.block.Transactions[0].TxHash(), e.Tx.TxHash())
		}

		// All tests always match against the second input or output.
		if e.Index != 1 {
			t.Fatalf("unexpected index match. want=%d got=%d",
				1, e.Index)
		}

		if e.Tree != tree {
			t.Fatalf("unexpected tree match. want=%d got=%d",
				tree, e.Tree)
		}
	}

	for _, c := range scannerTestCases {
		c := c
		ok := t.Run(c.name, func(t *testing.T) { runTC(c, t) })
		if !ok {
			break
		}
	}
}

// TestHistoricalCancellation tests that cancelling the search for a target
// before it's found makes it actually not get found.
func TestHistoricalCancellation(t *testing.T) {
	completeChan := make(chan struct{})
	cancelChan := make(chan struct{})
	foundChan := make(chan Event)
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Instrument the mock chain so we can stop the search mid-way through.
	tc.chain.sendNextCfilterChan = make(chan struct{})

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
		WithCancelChan(cancelChan),
	)

	// Allow the first 10 blocks to be scanned.
	for i := 0; i < 10; i++ {
		tc.chain.sendNextCfilterChan <- struct{}{}
	}

	// We don't expect the target to be found yet.
	select {
	case <-completeChan:
		t.Fatal("Unexpected completeChan receive")
	case <-foundChan:
		t.Fatal("Unexpected foundChan receive")
	case <-time.After(10 * time.Millisecond):
	}

	// Cancel the search.
	close(cancelChan)

	// The next cfilter may have been requested already, so we allow it to
	// send (or wait a bit to make sure it wasn't requested).
	select {
	case tc.chain.sendNextCfilterChan <- struct{}{}:
	case <-time.After(10 * time.Millisecond):
	}

	// We don't expect neither the complete chan, foundChan or new requests
	// for cfilters to be triggered.
	select {
	case <-tc.chain.sendNextCfilterChan:
		t.Fatal("Unexpected sendNextCfilterChan receive")
	case <-completeChan:
		t.Fatal("Unexpected completeChan receive")
	case <-foundChan:
		t.Fatal("Unexpected foundChan receive")
	default:
	}
}

// TestHistoricalStartHeight tests that starting the search after the height
// where the target is found makes it actually not get found.
func TestHistoricalStartHeight(t *testing.T) {
	completeChan := make(chan struct{})
	foundChan := make(chan Event)
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
		WithStartHeight(12),
	)

	// The completeChan should be signalled with the completion of the
	// scan.
	assertCompleted(t, completeChan)

	// foundChan should not have been triggered.
	assertFoundChanEmpty(t, foundChan)
}

// TestHistoricalEndHeight tests that stopping the search before the height
// where the target is found makes it actually not get found.
func TestHistoricalEndHeight(t *testing.T) {
	completeChan := make(chan struct{})
	foundChan := make(chan Event)
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
		WithEndHeight(int32(b.block.Header.Height-2)),
	)

	// The completeChan should be signalled with the completion of the
	// scan.
	assertCompleted(t, completeChan)

	// foundChan should not have been triggered.
	assertFoundChanEmpty(t, foundChan)
}

// TestHistoricalMultipleMatchesInBlock tests that the historical search
// correctly sends multiple events when the same script is confirmed multiple
// times in a single block.
func TestHistoricalMultipleMatchesInBlock(t *testing.T) {
	completeChan := make(chan struct{})
	foundChan := make(chan Event)
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	// Create an additional output.
	b.block.Transactions[0].AddTxOut(&wire.TxOut{PkScript: testPkScript})

	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
	)

	// The completeChan should be signalled with the completion of the
	// scan.
	assertCompleted(t, completeChan)

	// foundChan should be triggered two (and only two) times.
	e1 := assertFoundChanRcvHeight(t, foundChan, int32(b.block.Header.Height))
	e2 := assertFoundChanRcvHeight(t, foundChan, int32(b.block.Header.Height))
	assertFoundChanEmpty(t, foundChan)

	// However the events should *not* be exactly the same: the script was
	// confirmed in two different outputs.
	if e1 == e2 {
		t.Fatal("script confirmed twice in the same output")
	}
}

// TestHistoricalBlockDownload tests that the historical search only downloads
// blocks for which the cfilter has passed.
func TestHistoricalBlockDownload(t *testing.T) {
	completeChan := make(chan struct{})
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Perform the full search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithCompleteChan(completeChan),
	)
	assertCompleted(t, completeChan)

	// We only expect fetches for 11 blocks of data.
	wantGetBlockCount := uint32(11)
	if tc.chain.getBlockCount != wantGetBlockCount {
		t.Fatalf("Unexpected getBlockCount. want=%d got=%d",
			wantGetBlockCount, tc.chain.getBlockCount)
	}
}

// TestHistoricalMultipleFinds tests that performing a search with multiple
// finds for the same target works as expected.
func TestHistoricalMultipleFinds(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newHistTestCtx(t)
		defer tc.cleanup()

		// The generated test chain is:
		// - 5 blocks that miss the cfilter match
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		// - 5 blocks with a cfilter match
		tc.genBlocks(5, false)
		tc.genBlocks(5, true)
		b := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b)
		tc.genBlocks(5, true)

		foundChan1 := make(chan Event)
		foundChan2 := make(chan Event)

		// Start the search.
		tc.hist.FindMany([]TargetAndOptions{
			{
				Target: c.target(b),
				Options: []Option{
					WithFoundChan(foundChan1),
				},
			},
			{
				Target: c.target(b),
				Options: []Option{
					WithFoundChan(foundChan2),
				},
			},
		})

		// We expect one (and only one) event in each foundChan.
		assertFoundChanRcvHeight(tc.t, foundChan1, int32(b.block.Header.Height))
		assertFoundChanRcvHeight(tc.t, foundChan2, int32(b.block.Header.Height))
		assertFoundChanEmpty(tc.t, foundChan1)
		assertFoundChanEmpty(tc.t, foundChan2)
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := t.Run(c.name, func(t *testing.T) { runTC(c, t) })
		if !ok {
			break
		}
	}
}

// TestHistoricalMultipleOverlap tests that starting a search with multiple
// targets with overlapping search intervals works as expected.
func TestHistoricalMultipleOverlap(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newHistTestCtx(t)
		defer tc.cleanup()

		// The generated test chain is:
		// - 5 blocks that miss the cfilter match
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		tc.genBlocks(5, false)
		tc.genBlocks(5, true)
		b := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b)
		tc.genBlocks(5, true)
		b2 := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b2)

		foundChan1 := make(chan Event)
		foundChan2 := make(chan Event)
		completeChan1 := make(chan struct{})
		completeChan2 := make(chan struct{})

		// Start two concurrent searches with non-overlapping heights.
		tc.hist.FindMany([]TargetAndOptions{
			{
				Target: c.target(b),
				Options: []Option{
					WithFoundChan(foundChan1),
					WithCompleteChan(completeChan1),
					WithEndHeight(int32(b.block.Header.Height + 2)),
				},
			},
			{
				Target: c.target(b2),
				Options: []Option{
					WithFoundChan(foundChan2),
					WithCompleteChan(completeChan2),
					WithStartHeight(int32(b.block.Header.Height + 1)),
				},
			},
		})

		// Wait for both searches to complete.
		assertCompleted(tc.t, completeChan1)
		assertCompleted(tc.t, completeChan2)

		// We expect one (and only one) signall in each foundChan.
		assertFoundChanRcvHeight(tc.t, foundChan1, int32(b.block.Header.Height))
		assertFoundChanRcvHeight(tc.t, foundChan2, int32(b2.block.Header.Height))
		assertFoundChanEmpty(tc.t, foundChan1)
		assertFoundChanEmpty(tc.t, foundChan2)
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := t.Run(c.name, func(t *testing.T) { runTC(c, t) })
		if !ok {
			break
		}
	}
}

// TestHistoricalMultipleNoOverlap tests that starting a search with multiple,
// non-overlapping targets works as expected.
func TestHistoricalMultipleNoOverlap(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newHistTestCtx(t)
		defer tc.cleanup()

		// The generated test chain is:
		// - 5 blocks that miss the cfilter match
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		// - 5 blocks with a cfilter match
		// - block with test case manglers
		tc.genBlocks(5, false)
		tc.genBlocks(5, true)
		b := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b)
		tc.genBlocks(5, true)
		b2 := tc.chain.newFromTip(c.manglers...)
		tc.chain.extend(b2)

		foundChan1 := make(chan Event)
		foundChan2 := make(chan Event)
		completeChan1 := make(chan struct{})
		completeChan2 := make(chan struct{})

		// Start two concurrent searches with non-overlapping heights.
		tc.hist.FindMany([]TargetAndOptions{
			{
				Target: c.target(b),
				Options: []Option{
					WithFoundChan(foundChan1),
					WithCompleteChan(completeChan1),
					WithEndHeight(int32(b.block.Header.Height + 1)),
				},
			},
			{
				Target: c.target(b2),
				Options: []Option{
					WithFoundChan(foundChan2),
					WithCompleteChan(completeChan2),
					WithStartHeight(int32(b.block.Header.Height + 4)),
				},
			},
		})

		// Wait for both searches to complete.
		assertCompleted(tc.t, completeChan1)
		assertCompleted(tc.t, completeChan2)

		// We expect one (and only one) signall in each foundChan.
		assertFoundChanRcvHeight(tc.t, foundChan1, int32(b.block.Header.Height))
		assertFoundChanRcvHeight(tc.t, foundChan2, int32(b2.block.Header.Height))
		assertFoundChanEmpty(tc.t, foundChan1)
		assertFoundChanEmpty(tc.t, foundChan2)
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := t.Run(c.name, func(t *testing.T) { runTC(c, t) })
		if !ok {
			break
		}
	}
}

// TestHistoricalAddNewTargetDuringFcb tests that adding new targets for the
// historical search during the call for FoundCallback by using the passed
// function works as expected and provokes the new target to be found.
func TestHistoricalAddNewTargetDuringFcb(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newHistTestCtx(t)
		defer tc.cleanup()

		// The generated test chain is:
		// - 5 blocks that miss the cfilter match
		// - 5 blocks with a cfilter match
		// - block with test case manglers (twice)
		// - 5 blocks with a cfilter match
		tc.genBlocks(5, false)
		tc.genBlocks(5, true)
		b := tc.chain.newFromTip(c.manglers...)
		dupeTestTx(b)
		tc.chain.extend(b)
		tc.genBlocks(5, true)

		foundChan := make(chan Event)
		foundCb := func(e Event, addNew FindFunc) {
			assertNoError(t, addNew(
				c.target(b),
				WithStartHeight(e.BlockHeight+1),
				WithFoundChan(foundChan),
			))
		}

		// Start the search.
		tc.hist.Find(
			c.target(b),
			WithFoundCallback(foundCb),
		)

		// We expect one (and only one) event in foundChan
		assertFoundChanRcvHeight(t, foundChan, int32(b.block.Header.Height))
		assertFoundChanEmpty(t, foundChan)

	}
	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := t.Run(c.name, func(t *testing.T) { runTC(c, t) })
		if !ok {
			break
		}
	}
}

// TestHistoricalAddNewTarget tests that adding new targets for the historical
// search during the call for FoundCallback works as expected and provokes the
// new target to be found.
func TestHistoricalAddNewTarget(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	pkScript2 := []byte{0x01, 0x02, 0x03, 0x04}

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	// - block with second confirmed pkscript
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)
	b2 := tc.chain.newFromTip(
		confirmScript(pkScript2),
		cfilterData(pkScript2),
	)
	tc.chain.extend(b2)

	foundChanOld := make(chan Event)
	foundChanNew := make(chan Event)
	foundChanRestart := make(chan Event)
	foundCb := func(e Event, _ FindFunc) {
		// The foundCallback will start a new search for three
		// different targets:
		// - The testPkScript with startHeight of e.BlockHeight+1 which
		// shouldn't match;
		// - The pkScript2 with startHeight of e.BlockHeight+1 which
		// should match;
		// - The testPkScript with startHeight of 0 which should match.
		tc.hist.Find(
			ConfirmedScript(0, testPkScript),
			WithFoundChan(foundChanOld),
			WithStartHeight(e.BlockHeight+1),
		)
		tc.hist.Find(
			ConfirmedScript(0, pkScript2),
			WithFoundChan(foundChanNew),
			WithStartHeight(e.BlockHeight+1),
		)
		tc.hist.Find(
			ConfirmedScript(0, testPkScript),
			WithFoundChan(foundChanRestart),
		)
	}

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundCallback(foundCb),
	)

	// foundChanOld should be empty since the search was started at a block
	// height higher than where the script was confirmed. The other two
	// channels should have confirmations.
	assertFoundChanEmpty(t, foundChanOld)
	assertFoundChanRcvHeight(t, foundChanNew, int32(b2.block.Header.Height))
	assertFoundChanRcvHeight(t, foundChanRestart, int32(b.block.Header.Height))
}

// TestHistoricalAddNewTargetSingleBatch tests that adding new targets for the
// historical search during the call for FoundCallback which doesn't lead to a
// new batch correctly causes only the current batch to be executed.
func TestHistoricalAddNewTargetSingleBatch(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	// - block with second confirmed pkscript
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	completeChan := make(chan struct{})
	foundCb := func(e Event, _ FindFunc) {
		// Add the new target with a start height of currentHeight+1 so
		// that it will be added to the current batch.
		tc.hist.Find(
			ConfirmedScript(0, testPkScript),
			WithCompleteChan(completeChan),
			WithStartHeight(e.BlockHeight+1),
		)
	}

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundCallback(foundCb),
	)

	assertCompleted(t, completeChan)

	// We expect only a single batch to have taken place, so all blocks
	// should have been downloaded only once.
	wantGetBlockCount := uint32(11)
	if tc.chain.getBlockCount != wantGetBlockCount {
		t.Fatalf("Unexpected getBlockCount. want=%d got=%d",
			wantGetBlockCount, tc.chain.getBlockCount)
	}
}

// TestHistoricalNewTipNoOverlap tests that performing a historical search when
// new tips of the chain are coming in behaves as expected when we create a new
// search that does not overlap with the existing search.
func TestHistoricalNewTipNoOverlap(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// Instrument the mock chain so we can stop the search half-way
	// through.
	tc.chain.sendNextCfilterChan = make(chan struct{})

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	// - block with second confirmed pkscript
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Start the search.
	foundChan := make(chan Event)
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
	)

	// Let it process 3 blocks and start processing the fourth.
	tc.chain.sendNextCfilterChan <- struct{}{}
	tc.chain.sendNextCfilterChan <- struct{}{}
	tc.chain.sendNextCfilterChan <- struct{}{}

	// Let the blocks be processed.
	time.Sleep(10 * time.Millisecond)

	// Extend the tip with 3 new blocks, including a match of the target at
	// the end.
	startHeight := int32(tc.chain.tip.block.Header.Height) + 1
	tc.genBlocks(2, true)
	b2 := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b2)

	// Start a new search which does not overlap with the previous search.
	// The start height will be higher than the previous search's end
	// height.
	foundChanNew := make(chan Event)
	completeChanNew := make(chan struct{})
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChanNew),
		WithStartHeight(startHeight),
		WithCompleteChan(completeChanNew),
	)

	// Send as many blocks as needed until no more are requested by the
	// scan.
	done := false
	for !done {
		select {
		case tc.chain.sendNextCfilterChan <- struct{}{}:
		case <-time.After(10 * time.Millisecond):
			done = true
		}
	}

	// Wait for the second scan to wrap up.
	assertCompleted(tc.t, completeChanNew)

	// We expect one (and only one) signal in each foundChan.
	assertFoundChanRcvHeight(tc.t, foundChan, int32(b.block.Header.Height))
	assertFoundChanRcvHeight(tc.t, foundChanNew, int32(b2.block.Header.Height))
	assertFoundChanEmpty(tc.t, foundChan)
	assertFoundChanEmpty(tc.t, foundChanNew)
}

// TestHistoricalNewTipOverlap tests that performing a historical search when
// new tips of the chain are coming in behaves as expected when we create a new
// search that overlaps with the existing search.
func TestHistoricalNewTipOverlap(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// Instrument the mock chain so we can stop the search mid-way through.
	tc.chain.sendNextCfilterChan = make(chan struct{})

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	// - block with second confirmed pkscript
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	// Start the search.
	foundChan := make(chan Event)
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
	)

	// Let it process 3 blocks and start processing the fourth.
	tc.chain.sendNextCfilterChan <- struct{}{}
	tc.chain.sendNextCfilterChan <- struct{}{}
	tc.chain.sendNextCfilterChan <- struct{}{}

	// Let the blocks be processed.
	time.Sleep(10 * time.Millisecond)

	// Extend the tip with 3 new blocks, including a match of the target at
	// the end.
	startHeight := int32(13)
	tc.genBlocks(2, true)
	b2 := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b2)

	// Start a new search which overlaps with the previous search. The
	// start height will be lower than the previous search's end height and
	// its current height.
	foundChanNew := make(chan Event)
	completeChanNew := make(chan struct{})
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChanNew),
		WithStartHeight(startHeight),
		WithCompleteChan(completeChanNew),
	)

	// Send as many blocks as needed until no more are requested by the
	// scan.
	done := false
	for !done {
		select {
		case tc.chain.sendNextCfilterChan <- struct{}{}:
		case <-time.After(10 * time.Millisecond):
			done = true
		}
	}

	// Wait for the second scan to wrap up.
	assertCompleted(tc.t, completeChanNew)

	// We expect one (and only one) signal in each foundChan.
	assertFoundChanRcvHeight(tc.t, foundChan, int32(b.block.Header.Height))
	assertFoundChanRcvHeight(tc.t, foundChanNew, int32(b2.block.Header.Height))
	assertFoundChanEmpty(tc.t, foundChan)
	assertFoundChanEmpty(tc.t, foundChanNew)

	// We only expect one fetch for each (cfilter matched) block.
	wantGetBlockCount := uint32(14)
	if tc.chain.getBlockCount != wantGetBlockCount {
		t.Fatalf("Unexpected getBlockCount. want=%d got=%d",
			wantGetBlockCount, tc.chain.getBlockCount)
	}
}

// TestHistoricalAfterTip tests that attempting to scan past the current chain
// tip works as expected.
func TestHistoricalAfterTip(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with test case manglers
	// - 5 blocks with a cfilter match
	// - block with second confirmed pkscript
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	b := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	tc.chain.extend(b)
	tc.genBlocks(5, true)

	completeChan := make(chan struct{})
	foundChan := make(chan Event)

	// Start the search with an EndHeight past the tip. The mock chain
	// returns ErrBlockAfterTip in this situation.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
		WithEndHeight(int32(tc.chain.tip.block.Header.Height)+1),
	)

	assertCompleted(t, completeChan)
	assertFoundChanRcvHeight(t, foundChan, int32(b.block.Header.Height))
}

// TestHistoricalFindSpendAfterConfirm asserts that attempting to find a spent
// UTXO works when the spend happens on a later block and the spent is added
// during the search for confirmation.
func TestHistoricalFindSpendAfterConfirm(t *testing.T) {
	tc := newHistTestCtx(t)
	defer tc.cleanup()

	// The generated test chain is:
	// - 5 blocks that miss the cfilter match
	// - 5 blocks with a cfilter match
	// - block with confirmed output
	// - 5 blocks with a cfilter match
	// - 5 blocks that miss the cfilter match
	// - block which spends the previous output
	tc.genBlocks(5, false)
	tc.genBlocks(5, true)
	bConfirm := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	outp := wire.OutPoint{
		Hash:  bConfirm.block.Transactions[0].TxHash(),
		Index: 1,
	}
	tc.chain.extend(bConfirm)
	tc.genBlocks(5, true)
	tc.genBlocks(5, false)
	bSpend := tc.chain.newFromTip(
		spendOutPoint(outp),
		cfilterData(testPkScript),
	)
	tc.chain.extend(bSpend)

	// Setup the callback that is called when the output is confirmed and
	// which will trigger the search for the spent outpoint.
	foundSpentChan := make(chan Event)
	completeChan := make(chan struct{})
	foundCb := func(e Event, addNew FindFunc) {
		assertNoError(t, addNew(
			SpentOutPoint(outp, 0, testPkScript),
			WithStartHeight(e.BlockHeight+1),
			WithFoundChan(foundSpentChan),
			WithCompleteChan(completeChan),
		))
	}

	// Start the search.
	tc.hist.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundCallback(foundCb),
	)

	// Search should've been completed and the spending tx found.
	assertCompleted(t, completeChan)
	assertFoundChanRcvHeight(t, foundSpentChan, int32(bSpend.block.Header.Height))
}

// BenchHistoricalCfilterMisses benchmarks the behavior of historical searches
// when most/all blocks cause a cfilter check to miss (that is, full blocks
// aren't downloaded and tested individually).
//
// Reported time and allocation count should be interpreted as per-block in the
// blockchain.
func BenchmarkHistoricalCfilterMisses(b *testing.B) {

	runTC := func(c scannerTestCase, b *testing.B) {
		b.ReportAllocs()
		tc := newHistTestCtx(b)
		defer tc.cleanup()

		// Dummy block (will never match as we won't add it to the
		// chain).
		bl := tc.chain.newFromTip(c.manglers...)

		// Generate N blocks without cfilter matches.
		tc.genBlocks(b.N, false)
		completeChan := make(chan struct{})
		foundChan := make(chan Event)

		targets := make([]TargetAndOptions, 1)
		targets[0] = TargetAndOptions{
			Target: c.target(bl),
			Options: []Option{
				WithCompleteChan(completeChan),
				WithFoundChan(foundChan),
			},
		}

		// Reset the benchmark to this point.
		b.ResetTimer()

		// Find all items and wait for them to complete.
		tc.hist.FindMany(targets)
		select {
		case <-foundChan:
			b.Fatal("Unexpected event in foundChan")
		case <-completeChan:
		case <-time.After(60 * time.Second):
			b.Fatal("Timeout in benchmark")
		}
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := b.Run(c.name, func(b *testing.B) { runTC(c, b) })
		if !ok {
			break
		}
	}
}

// BenchHistoricalMatches benchmarks the behavior of historical searches when
// most/all blocks cause a match in the block itself.
//
// Reported time and allocation counts should be interpreted as per-(1 input +
// 1 output) in the blockchain.
//
// Note: This benchmark only runs for testcases which might match multiple
// blocks (i.e., only match by script vs outpoint).
func BenchmarkHistoricalMatches(b *testing.B) {

	runTC := func(c scannerTestCase, b *testing.B) {
		b.ReportAllocs()
		tc := newHistTestCtx(b)
		defer tc.cleanup()

		bl := tc.chain.newFromTip(c.manglers...)

		// Generate N blocks with block matches.
		tc.chain.genBlocks(b.N, c.manglers...)
		completeChan := make(chan struct{})
		foundChan := make(chan Event)
		var cbCount int
		foundCb := func(_ Event, _ FindFunc) {
			cbCount++
		}

		// Drain foundChan until completeChan is closed.
		go func() {
			for {
				select {
				case <-foundChan:
				case <-completeChan:
					return
				}
			}
		}()

		targets := make([]TargetAndOptions, 1)
		targets[0] = TargetAndOptions{
			Target: c.target(bl),
			Options: []Option{
				WithCompleteChan(completeChan),
				WithFoundCallback(foundCb),
				WithFoundChan(foundChan),
			},
		}

		// Reset the benchmark to this point.
		b.ResetTimer()

		// Find all items and wait for them to complete.
		tc.hist.FindMany(targets)
		select {
		case <-completeChan:
			/*
				if cbCount != b.N {
					b.Fatalf("Different number of callback calls. want=%d got=%d",
						b.N, cbCount)
				}
			*/
		case <-time.After(60 * time.Second):
			b.Fatal("Timeout in benchmark")
		}
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		// The only test cases amenable to this benchmark are those
		// that use a fixed script or outpoint for matching.
		//
		// Returning multiple matches against a specific spent outpoint
		// isn't really something that should happen in the blockchain
		// but is useful as a benchmark method.
		if c.name != "SpentScript" && c.name != "ConfirmedScript" && c.name != "SpentOutPoint" {
			continue
		}

		c := c
		ok := b.Run(c.name, func(b *testing.B) { runTC(c, b) })
		if !ok {
			break
		}
	}
}

// BenchHistorical benchmarks the behavior of historical searches when most/all
// blocks cause a cfilter check to match (that is, full blocks are downloaded
// and tested individually).
//
// Reported time and allocation should be interpreted as per-(1 input + 1
// output) in the blockchain.
func BenchmarkHistorical(b *testing.B) {

	runTC := func(c scannerTestCase, b *testing.B) {
		b.ReportAllocs()
		tc := newHistTestCtx(b)
		defer tc.cleanup()

		// Dummy block (will never match as we won't add it to the
		// chain).
		bl := tc.chain.newFromTip(c.manglers...)

		// Generate N blocks with cfilter matches.
		tc.genBlocks(b.N, true)
		completeChan := make(chan struct{})
		foundChan := make(chan Event)

		targets := make([]TargetAndOptions, 1)
		targets[0] = TargetAndOptions{
			Target: c.target(bl),
			Options: []Option{
				WithCompleteChan(completeChan),
				WithFoundChan(foundChan),
			},
		}

		// Reset the benchmark to this point.
		b.ResetTimer()

		// Find all items and wait for them to complete.
		tc.hist.FindMany(targets)
		select {
		case <-foundChan:
			b.Fatal("Unexpected event in foundChan")
		case <-completeChan:
		case <-time.After(60 * time.Second):
			b.Fatal("Timeout in benchmark")
		}
	}

	// Test against all variants of targets.
	for _, c := range scannerTestCases {
		if !c.wantFound {
			continue
		}
		c := c
		ok := b.Run(c.name, func(b *testing.B) { runTC(c, b) })
		if !ok {
			break
		}
	}
}
