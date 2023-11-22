package chainscan

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/gcs/v4/blockcf2"
	"github.com/decred/dcrd/wire"
)

var (
	testBlockCounter uint32

	testPkScript = []byte{
		0x76, 0xa9, 0x14, 0x2b, 0xf4, 0x0a, 0x13, 0xea,
		0x4f, 0xf1, 0xf9, 0xd3, 0x5a, 0x54, 0x15, 0xb0,
		0xf7, 0x7d, 0x9e, 0x5d, 0xd9, 0x3b, 0x23, 0x88,
		0xac,
	}
	testSigScript = []byte{
		0x47, 0x30, 0x44, 0x2, 0x20, 0x7b, 0xe8, 0x26,
		0xed, 0x10, 0x5f, 0xaf, 0x88, 0xb1, 0x7d, 0x1,
		0x72, 0x43, 0xd, 0x76, 0x3a, 0x9a, 0x14, 0x99,
		0x8f, 0x2a, 0x96, 0x12, 0x4b, 0x7c, 0x70, 0x4d,
		0xa1, 0x4d, 0x96, 0x20, 0xc2, 0x2, 0x20, 0x45,
		0x8e, 0xf, 0x98, 0x11, 0x3b, 0x35, 0xa2, 0x2d,
		0x43, 0xfc, 0x2, 0xdf, 0xa4, 0xe, 0x17, 0x8,
		0xf7, 0xd8, 0xc7, 0x5f, 0xfe, 0xcd, 0x8e, 0x50,
		0x2c, 0xfd, 0x58, 0xda, 0x4a, 0x2b, 0x10, 0x1,
		0x21, 0x2, 0x6e, 0xa2, 0xca, 0x26, 0x16, 0x56,
		0xe6, 0xbc, 0xae, 0xd9, 0xb2, 0x32, 0xdc, 0xc4,
		0x7b, 0x30, 0x33, 0x20, 0x41, 0xbc, 0x31, 0xd5,
		0x3c, 0x40, 0xa8, 0xa1, 0x4a, 0xb9, 0x60, 0x4f,
		0x25, 0x33,
	}
	testOutPoint = wire.OutPoint{
		Hash: chainhash.Hash{
			0x72, 0x43, 0x0d, 0x76, 0x3a, 0x9a, 0x14, 0x99,
			0x8f, 0x2a, 0x96, 0x12, 0x4b, 0x7c, 0x70, 0x4d,
			0x43, 0xfc, 0x02, 0xdf, 0xa4, 0x0e, 0x17, 0x08,
			0xf7, 0xd8, 0xc7, 0x5f, 0xfe, 0xcd, 0x8e, 0x50,
		},
		Index: 1701,
	}

	emptyEvent Event
)

// testingIntf matches both *testing.T and *testing.B
type testingIntf interface {
	Fatal(...interface{})
	Fatalf(string, ...interface{})
	Helper()
}

func assertFoundChanRcv(t testingIntf, c chan Event) Event {
	t.Helper()

	var e Event
	select {
	case e = <-c:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for receive in foundChan")
	}
	return e
}

func assertFoundChanRcvHeight(t testingIntf, c chan Event, wantHeight int32) Event {
	t.Helper()

	e := assertFoundChanRcv(t, c)
	if e.BlockHeight != wantHeight {
		t.Fatalf("Unexpected BlockHeight in foundChan event. want=%d got=%d",
			wantHeight, e.BlockHeight)
	}
	return e
}

func assertFoundChanEmpty(t testingIntf, c chan Event) {
	t.Helper()
	select {
	case <-c:
		t.Fatal("Unexpected signal in foundChan")
	case <-time.After(10 * time.Millisecond):
	}
}

func assertStartWatchHeightSignalled(t testingIntf, c chan int32) int32 {
	t.Helper()

	var endHeight int32
	select {
	case endHeight = <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for start watch height chan signal")
	}
	return endHeight
}

func assertNoError(t testingIntf, err error) {
	t.Helper()

	if err == nil {
		return
	}

	t.Fatalf("Unexpected error: %v", err)
}

// assertCompleted asserts that the 'c' chan (which should be a completeChan
// passed to a scanner) is closed in a reasonable time.
func assertCompleted(t testingIntf, c chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for completeChan to close")
	}
}

type testBlock struct {
	block     *wire.MsgBlock
	blockHash chainhash.Hash
	cfilter   *gcs.FilterV2
	cfKey     [16]byte
}

type blockMangler struct {
	txMangler    func(tx *wire.MsgTx)
	blockMangler func(b *wire.MsgBlock)
	cfilterData  []byte
}

func spendOutPoint(outp wire.OutPoint) blockMangler {
	return blockMangler{
		txMangler: func(tx *wire.MsgTx) {
			tx.AddTxIn(wire.NewTxIn(&outp, 0, nil))
		},
	}
}

func spendScript(sigScript []byte) blockMangler {
	var outp wire.OutPoint
	rand.Read(outp.Hash[:])
	return blockMangler{
		txMangler: func(tx *wire.MsgTx) {
			tx.AddTxIn(wire.NewTxIn(&outp, 0, sigScript))
		},
	}
}

func confirmScript(pkScript []byte) blockMangler {
	return blockMangler{
		txMangler: func(tx *wire.MsgTx) {
			tx.AddTxOut(wire.NewTxOut(0, pkScript))
		},
	}
}

func moveRegularToStakeTree() blockMangler {
	return blockMangler{
		blockMangler: func(b *wire.MsgBlock) {
			b.STransactions = b.Transactions
			b.Transactions = make([]*wire.MsgTx, 0)
		},
	}
}

func cfilterData(pkScript []byte) blockMangler {
	return blockMangler{
		cfilterData: pkScript,
	}
}

func newTestBlock(height int32, prevBlock chainhash.Hash, blockManglers ...blockMangler) *testBlock {
	bc := atomic.AddUint32(&testBlockCounter, 1)
	tx := &wire.MsgTx{
		TxIn: []*wire.TxIn{
			{
				PreviousOutPoint: wire.OutPoint{
					Index: bc, // Ensure unique tx hash
				},
			},
		},
		TxOut: []*wire.TxOut{
			{
				PkScript: []byte{0x6a},
			},
		},
	}
	var cfData [][]byte
	for _, m := range blockManglers {
		if m.txMangler != nil {
			m.txMangler(tx)
		}
		if m.cfilterData != nil {
			cfData = append(cfData, m.cfilterData)
		}
	}

	block := &wire.MsgBlock{
		Header: wire.BlockHeader{
			PrevBlock: prevBlock,
			Height:    uint32(height),
			Nonce:     testBlockCounter,
		},
		Transactions: []*wire.MsgTx{tx},
	}
	binary.LittleEndian.PutUint32(block.Header.MerkleRoot[:], bc)

	for _, m := range blockManglers {
		if m.blockMangler != nil {
			m.blockMangler(block)
		}
	}

	var cfKey [16]byte
	copy(cfKey[:], block.Header.MerkleRoot[:])
	cf, err := gcs.NewFilterV2(blockcf2.B, blockcf2.M, cfKey, cfData)
	if err != nil {
		panic(err)
	}

	return &testBlock{
		block:     block,
		blockHash: block.BlockHash(),
		cfilter:   cf,
		cfKey:     cfKey,
	}
}

// dupeTestTx duplicates the first transaction of the test block (in either the
// regular or stake transaction trees).
func dupeTestTx(b *testBlock) {
	if len(b.block.Transactions) > 0 {
		b.block.Transactions = append(b.block.Transactions, b.block.Transactions[0])
	} else {
		b.block.STransactions = append(b.block.STransactions, b.block.STransactions[0])
	}
}

type mockChain struct {
	// blocks and byHeight are sync.Maps to avoid triggering the race
	// detector for the chain.
	blocks   *sync.Map // map[chainhash.Hash]*testBlock
	byHeight *sync.Map // map[int32]*testBlock

	tipMtx sync.Mutex
	tip    *testBlock

	mtx          sync.Mutex
	eventReaders []*eventReader

	newTipChan          chan struct{}
	getBlockCount       uint32
	getCfilterCount     uint32
	sendNextCfilterChan chan struct{}
}

func newMockChain() *mockChain {
	return &mockChain{
		blocks:     &sync.Map{},
		byHeight:   &sync.Map{},
		newTipChan: make(chan struct{}),
	}
}

func (mc *mockChain) newFromTip(manglers ...blockMangler) *testBlock {
	var height int32
	var prevBlock chainhash.Hash
	mc.tipMtx.Lock()
	if mc.tip != nil {
		height = int32(mc.tip.block.Header.Height + 1)
		prevBlock = mc.tip.block.BlockHash()
	}
	mc.tipMtx.Unlock()

	b := newTestBlock(height, prevBlock, manglers...)
	bh := b.block.BlockHash()
	mc.blocks.Store(bh, b)
	mc.byHeight.Store(height, b)
	return b
}

func (mc *mockChain) extend(b *testBlock) {
	mc.tipMtx.Lock()
	mc.tip = b
	mc.tipMtx.Unlock()
}

func (mc *mockChain) signalNewTip() {
	mc.mtx.Lock()
	readers := mc.eventReaders
	mc.mtx.Unlock()

	mc.tipMtx.Lock()
	tip := mc.tip
	mc.tipMtx.Unlock()

	e := BlockConnectedEvent{
		Hash:     tip.blockHash,
		Height:   int32(tip.block.Header.Height),
		PrevHash: tip.block.Header.PrevBlock,
		CFKey:    tip.cfKey,
		Filter:   tip.cfilter,
	}
	for _, r := range readers {
		select {
		case <-r.ctx.Done():
		case r.c <- e:
		}
	}
}

func (mc *mockChain) genBlocks(n int, manglers ...blockMangler) {
	for i := 0; i < n; i++ {
		mc.extend(mc.newFromTip(manglers...))
	}
}

func (mc *mockChain) ChainEvents(ctx context.Context) <-chan ChainEvent {
	r := &eventReader{
		ctx: ctx,
		c:   make(chan ChainEvent),
	}
	mc.mtx.Lock()
	mc.eventReaders = append(mc.eventReaders, r)
	mc.mtx.Unlock()
	return r.c
}

func (mc *mockChain) NextTip(ctx context.Context) (*chainhash.Hash, int32, [16]byte, *gcs.FilterV2, error) {
	select {
	case <-ctx.Done():
		return nil, 0, [16]byte{}, nil, ctx.Err()
	case <-mc.newTipChan:
		mc.tipMtx.Lock()
		tip := mc.tip
		mc.tipMtx.Unlock()
		return &tip.blockHash, int32(tip.block.Header.Height), tip.cfKey, tip.cfilter, nil
	}
}

func (mc *mockChain) GetBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
	if b, ok := mc.blocks.Load(*bh); ok {
		atomic.AddUint32(&mc.getBlockCount, 1)
		return b.(*testBlock).block, nil
	}
	return nil, errors.New("block not found")
}

func (mc *mockChain) CurrentTip(ctx context.Context) (*chainhash.Hash, int32, error) {
	mc.tipMtx.Lock()
	tip := mc.tip
	mc.tipMtx.Unlock()

	if tip == nil {
		return nil, 0, errors.New("chain uninitialized")
	}

	return &tip.blockHash, int32(tip.block.Header.Height), nil
}

func (mc *mockChain) GetCFilter(ctx context.Context, height int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error) {
	// If sendNextCfilter is specified then wait until it's signalled by
	// the test routine to send the cfilter.
	if mc.sendNextCfilterChan != nil {
		<-mc.sendNextCfilterChan
	}

	mc.tipMtx.Lock()
	tip := mc.tip
	mc.tipMtx.Unlock()
	if tip == nil {
		return nil, [16]byte{}, nil, errors.New("chain unitialized")
	}

	if height > int32(tip.block.Header.Height) {
		return nil, [16]byte{}, nil, ErrBlockAfterTip{Height: height}
	}

	bl, ok := mc.byHeight.Load(height)
	if !ok {
		return nil, [16]byte{}, nil, fmt.Errorf("unknown block by height %d", height)
	}
	block := bl.(*testBlock)
	atomic.AddUint32(&mc.getCfilterCount, 1)
	return &block.blockHash, block.cfKey, block.cfilter, nil
}
