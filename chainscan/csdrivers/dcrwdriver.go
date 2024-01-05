package csdrivers

import (
	"context"
	"sync"
	"time"

	"decred.org/dcrwallet/v3/errors"
	"decred.org/dcrwallet/v3/wallet"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainscan"
)

type cfilter struct {
	height int32
	hash   chainhash.Hash
	key    [16]byte
	filter *gcs.FilterV2
}

type eventReader struct {
	ctx context.Context
	c   chan chainscan.ChainEvent
}

// DcrwalletCSDriver...
//
// NOTE: Using an individual driver instance for more than either a single
// historical or tip watcher scanner leads to undefined behavior.
type DcrwalletCSDriver struct {
	w *wallet.Wallet

	blockCache *blockcache.BlockCache

	mtx      sync.Mutex
	ereaders []*eventReader

	// The following are members that define a cfilter cache which is
	// useful to reduce db contention by reading cfilters in batches.
	cache            []cfilter
	cacheStartHeight int32
}

// Type assertions to ensure the driver fulfills the correct interfaces.
var _ chainscan.HistoricalChainSource = (*DcrwalletCSDriver)(nil)
var _ chainscan.TipChainSource = (*DcrwalletCSDriver)(nil)

// Hint for the capacity of the cache size. Value is arbitrary at this point.
var cacheCapHint = 200

func NewDcrwalletCSDriver(w *wallet.Wallet, bCache *blockcache.BlockCache) *DcrwalletCSDriver {
	return &DcrwalletCSDriver{
		w:          w,
		blockCache: bCache,
		cache:      make([]cfilter, 0, cacheCapHint),
	}
}

func (d *DcrwalletCSDriver) signalEventReaders(e chainscan.ChainEvent) {
	d.mtx.Lock()
	readers := d.ereaders
	d.mtx.Unlock()

	for _, er := range readers {
		select {
		case <-er.ctx.Done():
		case er.c <- e:
		}
	}
}

// Run runs the driver. It should be executed on a separate goroutine. This is
// only needed if the NextTip() function is going to be used such as when using
// this driver as a source for a TipWatcher scanner.
func (d *DcrwalletCSDriver) Run(ctx context.Context) error {
	log.Debugf("Running chainscan driver with embedded dcrwallet")
	client := d.w.NtfnServer.TransactionNotifications()
	defer client.Done()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ntfn := <-client.C:
			for _, b := range ntfn.DetachedBlocks {
				e := chainscan.BlockDisconnectedEvent{
					Hash:     b.BlockHash(),
					Height:   int32(b.Height),
					PrevHash: b.PrevBlock,
					Header:   b,
				}

				d.signalEventReaders(e)
			}

			for _, b := range ntfn.AttachedBlocks {
				if b.Header == nil {
					// Shouldn't happen, but play it safe.
					continue
				}

				hash := b.Header.BlockHash()
				key, filter, err := d.w.CFilterV2(ctx, &hash)
				if err != nil {
					log.Errorf("Error obtaining cfilter from wallet: %v", err)
					return err
				}

				e := chainscan.BlockConnectedEvent{
					PrevHash: b.Header.PrevBlock,
					Hash:     hash,
					Height:   int32(b.Header.Height),
					CFKey:    key,
					Filter:   filter,
					Header:   b.Header,
				}

				d.signalEventReaders(e)
			}
		}
	}

}

func (d *DcrwalletCSDriver) ChainEvents(ctx context.Context) <-chan chainscan.ChainEvent {
	er := &eventReader{
		ctx: ctx,
		c:   make(chan chainscan.ChainEvent),
	}
	d.mtx.Lock()
	d.ereaders = append(d.ereaders, er)
	d.mtx.Unlock()

	return er.c
}

// GetBlock returns the given block for the given blockhash. Note that for
// wallets running in SPV mode this blocks until the wallet is connected to a
// peer and it correctly returns a full block.
func (d *DcrwalletCSDriver) GetBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
	return d.blockCache.GetBlock(ctx, bh, d.getBlock)
}

// getBlock returns the given block for the given blockhash. Note that for
// wallets running in SPV mode this blocks until the wallet is connected to a
// peer and it correctly returns a full block.
func (d *DcrwalletCSDriver) getBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
getblock:
	for {
		// Keep trying to get the network backend until the context is
		// canceled.
		n, err := d.w.NetworkBackend()
		if errors.Is(err, errors.NoPeers) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
				continue getblock
			}
		}

		blocks, err := n.Blocks(ctx, []*chainhash.Hash{bh})
		if len(blocks) > 0 && err == nil {
			return blocks[0], nil
		}

		// The syncer might have failed due to any number of reasons,
		// but it's likely it will come back online shortly. So wait
		// until we can try again.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (d *DcrwalletCSDriver) CurrentTip(ctx context.Context) (*chainhash.Hash, int32, error) {
	bh, h := d.w.MainChainTip(ctx)
	return &bh, h, nil
}

// GetCFilter is part of the chainscan.HistoricalChainSource interface.
//
// NOTE: The returned chainhash pointer is not safe for storage as it belongs
// to a cache entry. This is fine for use on a chainscan.Historical scanner
// since it never stores or leaks the pointer itself.
func (d *DcrwalletCSDriver) GetCFilter(ctx context.Context, height int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error) {
	// Fast track when data is in memory.
	if height >= d.cacheStartHeight && height < d.cacheStartHeight+int32(len(d.cache)) {
		i := int(height - d.cacheStartHeight)
		c := &d.cache[i]
		return &c.hash, c.key, c.filter, nil
	}

	// Read a bunch of cfilters in one go since it's likely we'll be
	// queried for the next few.
	start := wallet.NewBlockIdentifierFromHeight(height)
	i := 0
	d.cache = d.cache[:cap(d.cache)]
	rangeFn := func(bh chainhash.Hash, key [16]byte, filter *gcs.FilterV2) (bool, error) {
		d.cache[i] = cfilter{
			hash:   bh,
			height: height + int32(i),
			key:    key,
			filter: filter,
		}

		// Stop if the cache has been filled.
		i++
		return i >= cap(d.cache), nil
	}
	err := d.w.RangeCFiltersV2(ctx, start, nil, rangeFn)
	if err != nil {
		return nil, [16]byte{}, nil, err
	}

	// If we didn't read any filters from the db, it means we were
	// requested a filter past the current mainchain tip. Inform the
	// appropriate error in this case.
	if i == 0 {
		return nil, [16]byte{}, nil, chainscan.ErrBlockAfterTip{Height: height}
	}

	// Clear out unused entries so we don't keep a reference to the filters
	// forever.
	for j := i; j < cap(d.cache); j++ {
		d.cache[j].filter = nil
	}

	// Keep track of correct cache start and size.
	d.cache = d.cache[:i]
	d.cacheStartHeight = height

	// The desired filter is the first one.
	c := &d.cache[0]
	return &c.hash, c.key, c.filter, nil
}
