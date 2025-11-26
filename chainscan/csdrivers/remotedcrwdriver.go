package csdrivers

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"decred.org/dcrwallet/v5/rpc/walletrpc"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/gcs/v4"
	"github.com/decred/dcrd/gcs/v4/blockcf2"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/blockcache"
	"github.com/decred/dcrlnd/chainscan"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type RemoteWalletCSDriver struct {
	wsvc walletrpc.WalletServiceClient
	nsvc walletrpc.NetworkServiceClient

	blockCache *blockcache.BlockCache

	mtx      sync.Mutex
	ereaders []*eventReader

	// The following are members that define a cfilter cache which is
	// useful to reduce db contention by reading cfilters in batches.
	cache            []cfilter
	cacheStartHeight int32
}

// Type assertions to ensure the driver fulfills the correct interfaces.
var _ chainscan.HistoricalChainSource = (*RemoteWalletCSDriver)(nil)
var _ chainscan.TipChainSource = (*RemoteWalletCSDriver)(nil)

func NewRemoteWalletCSDriver(wsvc walletrpc.WalletServiceClient,
	nsvc walletrpc.NetworkServiceClient, bcache *blockcache.BlockCache) *RemoteWalletCSDriver {
	return &RemoteWalletCSDriver{
		wsvc:       wsvc,
		nsvc:       nsvc,
		cache:      make([]cfilter, 0, cacheCapHint),
		blockCache: bcache,
	}
}

func (d *RemoteWalletCSDriver) signalEventReaders(e chainscan.ChainEvent) {
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

func (d *RemoteWalletCSDriver) singleCFilter(ctx context.Context, hash *chainhash.Hash) ([16]byte, *gcs.FilterV2, error) {
	req := &walletrpc.GetCFiltersRequest{
		StartingBlockHash: hash[:],
		EndingBlockHash:   hash[:],
	}

	stream, err := d.wsvc.GetCFilters(ctx, req)
	if err != nil {
		return [16]byte{}, nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return [16]byte{}, nil, err
	}
	var key [16]byte

	filter, err := gcs.FromBytesV2(blockcf2.B, blockcf2.M, resp.Filter)
	if err != nil {
		return key, nil, err
	}

	copy(key[:], resp.Key)

	return key, filter, nil
}

func (d *RemoteWalletCSDriver) Run(ctx context.Context) error {
	req := &walletrpc.TransactionNotificationsRequest{}
	stream, err := d.wsvc.TransactionNotifications(ctx, req)
	if err != nil {
		return err
	}

	getHeaderReq := &walletrpc.BlockInfoRequest{}

nextntfn:
	for {
		var resp *walletrpc.TransactionNotificationsResponse
		resp, err = stream.Recv()
		if err != nil {
			break
		}

		for _, b := range resp.DetachedBlockHeaders {
			var hash, prevHash *chainhash.Hash
			hash, err = chainhash.NewHash(b.Hash)
			if err != nil {
				break nextntfn
			}
			prevHash, err = chainhash.NewHash(b.PrevBlock)
			if err != nil {
				break nextntfn
			}

			var headerRes *walletrpc.BlockInfoResponse
			getHeaderReq.BlockHash = hash[:]
			headerRes, err = d.wsvc.BlockInfo(ctx, getHeaderReq)
			if err != nil {
				break nextntfn
			}
			header := new(wire.BlockHeader)
			err = header.FromBytes(headerRes.BlockHeader)
			if err != nil {
				break nextntfn
			}

			e := chainscan.BlockDisconnectedEvent{
				Hash:     *hash,
				Height:   b.Height,
				PrevHash: *prevHash,
				Header:   header,
			}

			d.signalEventReaders(e)
		}

		for _, bl := range resp.AttachedBlocks {
			// Shouldn't happen, but play it safe.
			if bl.Height <= 0 {
				continue
			}

			var hash, prevHash *chainhash.Hash
			hash, err = chainhash.NewHash(bl.Hash)
			if err != nil {
				break nextntfn
			}
			prevHash, err = chainhash.NewHash(bl.PrevBlock)
			if err != nil {
				break nextntfn
			}

			var cfKey [16]byte
			var filter *gcs.FilterV2
			cfKey, filter, err = d.singleCFilter(ctx, hash)
			if err != nil {
				break nextntfn
			}

			var headerRes *walletrpc.BlockInfoResponse
			getHeaderReq.BlockHash = hash[:]
			headerRes, err = d.wsvc.BlockInfo(ctx, getHeaderReq)
			if err != nil {
				break nextntfn
			}
			header := new(wire.BlockHeader)
			err = header.FromBytes(headerRes.BlockHeader)
			if err != nil {
				break nextntfn
			}

			e := chainscan.BlockConnectedEvent{
				PrevHash: *prevHash,
				Hash:     *hash,
				Height:   bl.Height,
				CFKey:    cfKey,
				Filter:   filter,
				Header:   header,
			}
			d.signalEventReaders(e)
		}
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		log.Tracef("RemoteWalletCSDriver run context done: %v", err)
	} else if err != nil {
		log.Errorf("RemoteWalletCSDriver run errored: %v", err)
	}
	return err
}

func (d *RemoteWalletCSDriver) ChainEvents(ctx context.Context) <-chan chainscan.ChainEvent {
	er := &eventReader{
		ctx: ctx,
		c:   make(chan chainscan.ChainEvent),
	}
	d.mtx.Lock()
	d.ereaders = append(d.ereaders, er)
	d.mtx.Unlock()

	return er.c
}

func (d *RemoteWalletCSDriver) GetBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
	return d.blockCache.GetBlock(ctx, bh, d.getBlock)
}

func (d *RemoteWalletCSDriver) getBlock(ctx context.Context, bh *chainhash.Hash) (*wire.MsgBlock, error) {
	var (
		resp *walletrpc.GetRawBlockResponse
		err  error
	)

	req := &walletrpc.GetRawBlockRequest{
		BlockHash: bh[:],
	}

	// If the response error code is 'Unavailable' it means the wallet
	// isn't connected to any peers while in SPV mode. In that case, wait a
	// bit and try again.
	for stop := false; !stop; {
		resp, err = d.nsvc.GetRawBlock(ctx, req)
		switch {
		case status.Code(err) == codes.Unavailable:
			log.Warnf("Network unavailable from wallet; will try again in 5 seconds")
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
			}
		case err != nil:
			return nil, err
		default:
			stop = true
		}
	}

	bl := &wire.MsgBlock{}
	err = bl.FromBytes(resp.Block)
	if err != nil {
		return nil, err
	}

	return bl, nil
}

func (d *RemoteWalletCSDriver) CurrentTip(ctx context.Context) (*chainhash.Hash, int32, error) {
	resp, err := d.wsvc.BestBlock(ctx, &walletrpc.BestBlockRequest{})
	if err != nil {
		return nil, 0, err
	}
	bh, err := chainhash.NewHash(resp.Hash)
	if err != nil {
		return nil, 0, err
	}

	return bh, int32(resp.Height), nil
}

// GetCFilter is part of the chainscan.HistoricalChainSource interface.
//
// NOTE: The returned chainhash pointer is not safe for storage as it belongs
// to a cache entry. This is fine for use on a chainscan.Historical scanner
// since it never stores or leaks the pointer itself.
func (d *RemoteWalletCSDriver) GetCFilter(ctx context.Context, height int32) (*chainhash.Hash, [16]byte, *gcs.FilterV2, error) {
	// Fast track when data is in memory.
	if height >= d.cacheStartHeight && height < d.cacheStartHeight+int32(len(d.cache)) {
		i := int(height - d.cacheStartHeight)
		c := &d.cache[i]
		return &c.hash, c.key, c.filter, nil
	}

	// Use a new ctx so we can canel halfway through.
	ctxReq, cancel := context.WithCancel(ctx)
	defer cancel()

	// Read a bunch of CFilters in one go since we're likely to be
	// requested the next few ones.
	d.cache = d.cache[:cap(d.cache)]
	i := 0

tryconn:
	for {
		req := &walletrpc.GetCFiltersRequest{
			StartingBlockHeight: height + int32(i),
		}

		if i != 0 {
			log.Tracef("Attempting to refetch at %d",
				req.StartingBlockHeight)
		}

		stream, err := d.wsvc.GetCFilters(ctxReq, req)
		if status.Code(err) == codes.Unavailable {
			// Wallet may be temporarily offline. Backoff and try
			// again in a bit.
			select {
			case <-time.After(time.Second):
				continue
			case <-ctx.Done():
				return nil, [16]byte{}, nil, ctx.Err()
			}
		}
		if err != nil {
			return nil, [16]byte{}, nil, err
		}
		for {
			var key [16]byte
			resp, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break tryconn
			} else if status.Code(err) == codes.Unavailable {
				log.Tracef("Broke connection at height %d",
					req.StartingBlockHeight+int32(i))
				continue tryconn
			} else if err != nil {
				return nil, key, nil, err
			}

			bh, err := chainhash.NewHash(resp.BlockHash)
			if err != nil {
				return nil, key, nil, err
			}

			filter, err := gcs.FromBytesV2(blockcf2.B, blockcf2.M, resp.Filter)
			if err != nil {
				return nil, key, nil, err
			}
			copy(key[:], resp.Key)

			d.cache[i] = cfilter{
				hash:   *bh,
				height: height + int32(i),
				key:    key,
				filter: filter,
			}

			// Stop if the cache has been filled.
			i++
			if i >= cap(d.cache) {
				break tryconn
			}
		}
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
