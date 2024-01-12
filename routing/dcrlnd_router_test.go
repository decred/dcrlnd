package routing

import (
	"testing"

	"github.com/decred/dcrlnd/channeldb"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/stretchr/testify/require"
)

// TestForAllOutgoingFiltersNonOpen asserts the function only iterates over
// opened channels.
func TestForAllOutgoingFiltersNonOpen(t *testing.T) {
	const startingBlockHeight = 100
	ctx, cleanUp := createTestCtxFromFile(
		t, startingBlockHeight, basicGraphFilePath,
	)
	defer cleanUp()

	// Configure one channel as being opened. The others will be closed.
	ctx.router.cfg.LocalOpenChanIDs = func() (map[uint64]struct{}, error) {
		return map[uint64]struct{}{999991: {}}, nil
	}

	// Only a single channel should be returned by this function.
	var nbChans int
	err := ctx.router.ForAllOutgoingChannels(func(kvdb.RTx,
		*channeldb.ChannelEdgeInfo, *channeldb.ChannelEdgePolicy) error {
		nbChans += 1
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, nbChans)
}
