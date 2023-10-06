package cache_test

import (
	"crypto/rand"
	"testing"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/wire"
	cache "github.com/decred/dcrlnd/neutrinocache"
	"github.com/decred/dcrlnd/neutrinocache/lru"
)

// TestBlockFilterCaches tests that we can put and retrieve elements from all
// implementations of the filter and block caches.
func TestBlockFilterCaches(t *testing.T) {
	t.Parallel()

	// Create a cache large enough to not evict any item. We do this so we
	// don't have to worry about the eviction strategy of the tested
	// caches.
	const numElements = 10
	const cacheSize = 100000

	// Initialize all types of caches we want to test, for both filters and
	// blocks. Currently the LRU cache is the only implementation.
	blockCaches := []cache.Cache{lru.NewCache(cacheSize)}

	// Generate a list of hashes, filters and blocks that we will use as
	// cache keys an values.
	var (
		blockHashes []chainhash.Hash
		blocks      []*dcrutil.Block
	)
	for i := 0; i < numElements; i++ {
		var blockHash chainhash.Hash
		if _, err := rand.Read(blockHash[:]); err != nil {
			t.Fatalf("unable to read rand: %v", err)
		}

		blockHashes = append(blockHashes, blockHash)

		msgBlock := &wire.MsgBlock{}
		block := dcrutil.NewBlock(msgBlock)
		blocks = append(blocks, block)

		// Add the block to the block caches, using the block INV
		// vector as key.
		blockKey := wire.NewInvVect(
			wire.InvTypeBlock, &blockHash,
		)
		for _, c := range blockCaches {
			c.Put(*blockKey, &cache.CacheableBlock{block})
		}
	}

	// Now go through the list of block hashes, and make sure we can
	// retrieve all elements from the caches.
	for i, blockHash := range blockHashes {
		// Check block caches.
		blockKey := wire.NewInvVect(
			wire.InvTypeBlock, &blockHash,
		)
		for _, c := range blockCaches {
			b, err := c.Get(*blockKey)
			if err != nil {
				t.Fatalf("Unable to get block: %v", err)
			}

			// Ensure it is the same block.
			block := b.(*cache.CacheableBlock).Block
			if block != blocks[i] {
				t.Fatalf("Not equal: %v vs %v ",
					block, blocks[i])
			}
		}
	}
}
