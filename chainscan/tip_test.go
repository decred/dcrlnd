package chainscan

import (
	"context"
	"testing"
	"time"

	"github.com/decred/dcrd/wire"
)

type twTestCtx struct {
	chain  *mockChain
	tw     *TipWatcher
	cancel func()
	t      *testing.T
}

func newTwTestCtx(t *testing.T) *twTestCtx {
	ctx, cancel := context.WithCancel(context.Background())
	chain := newMockChain()
	tw := NewTipWatcher(chain)
	chain.extend(chain.newFromTip()) // Genesis block

	// Instrument tipProcessed.
	tw.tipProcessed = make(chan *blockCFilter)

	// Use synchronous version of Find() for tests by default.
	tw.syncFind = true

	go func() {
		tw.Run(ctx)
	}()
	return &twTestCtx{
		chain:  chain,
		tw:     tw,
		cancel: cancel,
		t:      t,
	}
}

func (t *twTestCtx) cleanup() {
	t.cancel()
}

func (t *twTestCtx) extendTipWait(b *testBlock) {
	t.t.Helper()
	t.chain.extend(b)
	t.chain.signalNewTip()
	select {
	case <-t.tw.tipProcessed:
	case <-time.After(5 * time.Second):
		t.t.Fatal("new tip not processed in time")
	}
}

func (t *twTestCtx) extendNewTip(manglers ...blockMangler) *testBlock {
	t.t.Helper()
	b := t.chain.newFromTip(manglers...)
	t.extendTipWait(b)
	return b
}

// TestTipWatcher tests the basic functionality of the TipWatcher by testing it
// against the scannerTestCases which must be fulfilled by both the tipWatcher
// and historical scanners.
func TestTipWatcher(t *testing.T) {
	runTC := func(c scannerTestCase, t *testing.T) {
		var foundCbEvent, foundChanEvent Event
		foundChan := make(chan Event)
		tc := newTwTestCtx(t)
		defer tc.cleanup()

		b := tc.chain.newFromTip(c.manglers...)
		err := tc.tw.Find(
			c.target(b),
			WithFoundCallback(func(e Event, _ FindFunc) { foundCbEvent = e }),
			WithFoundChan(foundChan),
		)
		if err != nil {
			t.Fatalf("Find returned error: %v", err)
		}

		tc.extendTipWait(b)

		if !c.wantFound {
			// Testing when we don't expect a match.

			assertFoundChanEmpty(t, foundChan)
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
		// block in either the stake or regular transaction tree.
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

// TestTipWatcherCancelation tests that cancelling the watch for a target works
// as expected.
func TestTipWatcherCancelation(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	foundChan := make(chan Event)
	cancelChan := make(chan struct{})
	completeChan := make(chan struct{})
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCancelChan(cancelChan),
		WithCompleteChan(completeChan),
	)

	// Mine a few blocks. We force a full block check to ensure the
	// behavior under false positives.
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))

	// Ensure none of the channels have been triggered yet
	select {
	case <-foundChan:
		t.Fatal("foundChan unexpectedly triggered")
	case <-cancelChan:
		t.Fatal("cancelChan unexpectedly triggered")
	case <-completeChan:
		t.Fatal("completeChan unexpectedly triggered")
	case <-time.After(time.Millisecond * 10):
	}

	// Generate a new block with a match. This should generate an event in
	// foundChan.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	assertFoundChanRcv(t, foundChan)

	// Cancel the request.
	close(cancelChan)

	// Generate a new block with a match. We don't expect this will trigger
	// foundChan given we just canceled the request.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	// Still don't expect a signal in complete and found chans.
	select {
	case <-foundChan:
		t.Fatal("foundChan unexpectedly triggered")
	case <-completeChan:
		t.Fatal("completeChan unexpectedly triggered")
	case <-time.After(time.Millisecond * 10):
	}

}

// TestTipWatcherStaleCompletion tests that watching for a target with a
// specific endHeight triggers completion.
func TestTipWatcherStaleCompletion(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	foundChan := make(chan Event)
	cancelChan := make(chan struct{})
	completeChan := make(chan struct{})
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCancelChan(cancelChan),
		WithCompleteChan(completeChan),
		WithEndHeight(5),
	)

	// Mine a few blocks. We force a full block check to ensure the
	// behavior under false positives.
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))

	// Ensure none of the channels have been triggered yet.
	select {
	case <-foundChan:
		t.Fatal("foundChan unexpectedly triggered")
	case <-cancelChan:
		t.Fatal("cancelChan unexpectedly triggered")
	case <-completeChan:
		t.Fatal("completeChan unexpectedly triggered")
	case <-time.After(time.Millisecond * 10):
	}

	// Generate a new block with a match. This should generate an event in
	// foundChan.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	assertFoundChanRcvHeight(t, foundChan, int32(tc.chain.tip.block.Header.Height))

	// Generate blocks until endHeight. The completeChan should be closed
	// by then.
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))
	assertCompleted(t, completeChan)

	// Generate a new block with a match. We don't expect this will trigger
	// foundChan given the request already completed.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	// Still don't expect a signal in found chans.
	assertFoundChanEmpty(t, foundChan)
}

// TestTipWatcherReorg tests that when a reorg occurs that causes the NextTip()
// function to go back to a previous height, watched targets are triggered even
// if they weren't triggered in the previous chain.
func TestTipWatcherReorg(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	foundChan := make(chan Event)
	completeChan := make(chan struct{})
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
		WithCompleteChan(completeChan),
	)

	// Mine a few blocks. We force a full block check to ensure the
	// behavior under false positives.
	forkRoot := tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))
	forkedTip := tc.extendNewTip(cfilterData(testPkScript))

	// Ensure none of the channels have been triggered yet.
	select {
	case <-foundChan:
		t.Fatal("foundChan unexpectedly triggered")
	case <-completeChan:
		t.Fatal("completeChan unexpectedly triggered")
	case <-time.After(time.Millisecond * 10):
	}

	// Force a reorg. We rewind the tip to the forkRoot point and generate
	// new blocks from there. The last generated block matches the desired
	// target at a height lower than the previous forked tip.
	tc.chain.tip = forkRoot
	tc.extendNewTip(cfilterData(testPkScript))
	newTip := tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	// foundChan should have been triggered.
	foundEvent := assertFoundChanRcv(t, foundChan)

	// The height of the match should be the new tip and this tip should be
	// at a height lower than the forked tip.
	wantHeight := int32(newTip.block.Header.Height)
	if foundEvent.BlockHeight != wantHeight {
		t.Fatalf("Event not triggered at correct height. want=%d got=%d",
			wantHeight, foundEvent.BlockHeight)
	}
	if wantHeight >= int32(forkedTip.block.Header.Height) {
		t.Fatalf("New tip has height higher than forked tip. new=%d forked=%d",
			wantHeight, forkedTip.block.Header.Height)
	}

	// Generate blocks until the new chain has a higher height than the
	// forked tip.
	for tc.chain.tip.block.Header.Height <= forkedTip.block.Header.Height {
		tc.extendNewTip(cfilterData(testPkScript))
	}

	// Finally generate a new match to ensure the TipWatcher is still
	// finding the target.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	select {
	case <-completeChan:
		t.Fatal("completeChan unexpectedly signalled")
	case <-foundChan:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for foundChan")
	}
}

// TestTipWatcherMultipleMatchesInBlock tests that the historical search
// correctly sends multiple events when the same script is confirmed multiple
// times in a single block.
func TestTipWatcherMultipleMatchesInBlock(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	foundChan := make(chan Event)
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundChan(foundChan),
	)

	newTip := tc.chain.newFromTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)
	// Create an additional output.
	newTip.block.Transactions[0].AddTxOut(&wire.TxOut{PkScript: testPkScript})
	tc.chain.extend(newTip)
	tc.chain.signalNewTip()

	// foundChan should be triggered two (and only two) times.
	e1 := assertFoundChanRcv(t, foundChan)
	e2 := assertFoundChanRcv(t, foundChan)
	assertFoundChanEmpty(t, foundChan)

	// However the events should *not* be exactly the same: the script was
	// confirmed in two different outputs.
	if e1 == e2 {
		t.Fatal("script confirmed twice in the same output")
	}
}

// TestTipWatcherBlockDownload tests that TipWatcher only downloads blocks for
// which the cfilter has passed.
func TestTipWatcherBlockDownload(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
	)

	// Extend the tip with 2 blocks where the cfilter matches the watched
	// content and 3 where it doesn't.
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip()
	tc.extendNewTip()
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip()

	// We only expect fetches for 2 blocks of data.
	wantGetBlockCount := uint32(2)
	if tc.chain.getBlockCount != wantGetBlockCount {
		t.Fatalf("Unexpected getBlockCount. want=%d got=%d",
			wantGetBlockCount, tc.chain.getBlockCount)
	}
}

// TestTipWatcherStartWatchHeight tests whether the correct height is returned
// when starting to watch for a target.
func TestTipWatcherStartWatchHeight(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	swhChan := make(chan int32)
	var gotHeight int32

	// Switch to the regular asynchronous version of Find() since that's
	// what is used in production.
	tc.tw.syncFind = false

	// Test watching when the chain is synced and "quiet".
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithStartWatchHeightChan(swhChan),
	)
	wantHeight := tc.chain.tip.block.Header.Height
	gotHeight = assertStartWatchHeightSignalled(t, swhChan)
	if wantHeight != uint32(gotHeight) {
		t.Fatalf("Unexpected start watching height. want=%d got=%d",
			wantHeight, gotHeight)
	}

	// Test watching when the target is watched for in the middle of tip
	// processing. Note we haven't read from t.tw.tipProcessed so the tip
	// still hasn't been fully processed.
	newTip := tc.chain.newFromTip(cfilterData(testPkScript))
	tc.chain.extend(newTip)
	tc.chain.signalNewTip()

	// Give it enough time for the tip to start being processed.
	time.Sleep(10 * time.Millisecond)

	// Try to find the target again.
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithStartWatchHeightChan(swhChan),
	)

	// Give it enough time to block.
	time.Sleep(10 * time.Millisecond)

	// Finish processing tip.
	select {
	case <-tc.tw.tipProcessed:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for tipProcessed")
	}

	// We should see the target being watched for after the _new_ tip (vs
	// the one that was in the middle of processing when we tried to add
	// the target).
	wantHeight = newTip.block.Header.Height
	gotHeight = assertStartWatchHeightSignalled(t, swhChan)
	if wantHeight != uint32(gotHeight) {
		t.Fatalf("Unexpected start watching height. want=%d got=%d",
			wantHeight, gotHeight)
	}
}

// TestTipWatcherMultipleFinds tests whether attempting to find multiple times
// the same target works as expected.
func TestTipWatcherMultipleFinds(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newTwTestCtx(t)
		defer tc.cleanup()

		b := tc.chain.newFromTip(c.manglers...)

		// Start two searches for the same pkscript.
		foundChan1 := make(chan Event)
		foundChan2 := make(chan Event)
		tc.tw.Find(
			c.target(b),
			WithFoundChan(foundChan1),
		)
		tc.tw.Find(
			c.target(b),
			WithFoundChan(foundChan2),
		)

		// Confirm it.
		tc.extendTipWait(b)

		// The two foundChans should be signalled.
		event1 := assertFoundChanRcvHeight(t, foundChan1, int32(b.block.Header.Height))
		event2 := assertFoundChanRcv(t, foundChan2)
		if event1 != event2 {
			t.Fatalf("Different events returned: %s vs %s", &event1, &event2)
		}
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

// TestTipWatcherAddNewTarget tests that adding a new target during a
// foundCallback works as expected.
func TestTipWatcherAddNewTarget(t *testing.T) {
	tc := newTwTestCtx(t)
	defer tc.cleanup()

	// Disable syncFind so Find() won't deadlock during foundCb.
	tc.tw.syncFind = false

	// foundChan should only be triggered in a block _after_ the foundCb
	// callback is called.
	cancelChan := make(chan struct{})
	foundChan := make(chan Event)
	swhChan := make(chan int32)
	foundCb := func(e Event, _ FindFunc) {
		close(cancelChan) // Prevent repeated calls of foundCb.
		tc.tw.Find(
			ConfirmedScript(0, testPkScript),
			WithFoundChan(foundChan),
			WithStartWatchHeightChan(swhChan),
		)
	}
	tc.tw.Find(
		ConfirmedScript(0, testPkScript),
		WithFoundCallback(foundCb),
		WithCancelChan(cancelChan),
	)

	// Give it enough time for the new target to register in the scanner.
	time.Sleep(10 * time.Millisecond)

	// Extend the chain with 2 blocks without the target script and then
	// one block with the target script (which triggers foundCb).
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(cfilterData(testPkScript))
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	// foundChan shoulnd't have been signalled yet.
	assertFoundChanEmpty(t, foundChan)

	// But the new search should have started.
	assertStartWatchHeightSignalled(t, swhChan)

	// Extend the chain with a new block with the taget script. We expect
	// foundChan to be triggered now, but only once.
	tc.extendNewTip(
		confirmScript(testPkScript),
		cfilterData(testPkScript),
	)

	assertFoundChanRcvHeight(t, foundChan, int32(tc.chain.tip.block.Header.Height))
	assertFoundChanEmpty(t, foundChan)
}

// TestTipWatcherAddNewTargetDuringFcb tests that adding a new target during a
// foundCallback to be searched starting at the same block works as expected.
func TestTipWatcherAddNewTargetDuringFcb(t *testing.T) {

	runTC := func(c scannerTestCase, t *testing.T) {
		tc := newTwTestCtx(t)
		defer tc.cleanup()

		b := tc.chain.newFromTip(c.manglers...)
		dupeTestTx(b)

		// The found callback is called for the main find below and
		// adds a new target that signals via foundChan.
		foundChan := make(chan Event)
		foundCb := func(e Event, addNew FindFunc) {
			assertNoError(t, addNew(
				c.target(b),
				WithStartHeight(e.BlockHeight+1),
				WithFoundChan(foundChan),
			))
		}

		tc.tw.Find(
			c.target(b),
			WithFoundCallback(foundCb),
		)

		// Confirm it.
		tc.extendTipWait(b)

		// We expect foundChan to receive one (and only one) event.
		assertFoundChanRcv(t, foundChan)
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
