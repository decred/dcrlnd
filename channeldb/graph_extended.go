package channeldb

import (
	"github.com/decred/dcrlnd/channeldb/kvdb"
)

var (
	// knownSpentBucket is the key of a top-level bucket that tracks
	// channels that are known to be spent by their short channel id.
	knownSpentBucket = []byte("channel-known-spent")
)

// MarkKnownSpent marks a channel as known to having been spent (i.e. closed)
// on-chain.
func (c *ChannelGraph) MarkKnownSpent(channelID uint64) error {
	return kvdb.Update(c.db, func(tx kvdb.RwTx) error {
		index, err := tx.CreateTopLevelBucket(knownSpentBucket)
		if err != nil {
			return err
		}
		var k [8]byte
		var v [1]byte = [1]byte{0x00}
		byteOrder.PutUint64(k[:], channelID)
		return index.Put(k[:], v[:])
	}, func() {})
}

// IsKnownSpent returns if the channel is known to be spent on-chain.
func (c *ChannelGraph) IsKnownSpent(channelID uint64) (bool, error) {
	var knownSpent bool
	err := kvdb.View(c.db, func(tx kvdb.RTx) error {
		index := tx.ReadBucket(knownSpentBucket)
		if index == nil {
			return nil
		}

		var k [8]byte
		byteOrder.PutUint64(k[:], channelID)
		v := index.Get(k[:])
		knownSpent = len(v) > 0 && v[0] == 0x00
		return nil
	}, func() { knownSpent = false })

	return knownSpent, err
}

// LocalOpenChanIDs returns a map of channel IDs of all open channels in the
// local DB.
func (c *ChannelGraph) LocalOpenChanIDs() (map[uint64]struct{}, error) {
	// Note: this is less efficient than it could be, because it iterates
	// through the entire list of channels and then discards all that just
	// to extract the channel id. In the future, decode that field directly.
	openChans, err := c.db.FetchAllOpenChannels()
	if err != nil {
		return nil, err
	}

	res := make(map[uint64]struct{}, len(openChans))
	for _, c := range openChans {
		res[c.ShortChanID().ToUint64()] = struct{}{}
	}
	return res, nil
}
