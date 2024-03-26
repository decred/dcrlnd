package dcrmigration02

import (
	"testing"
	"time"

	"github.com/decred/dcrlnd/channeldb/migtest"
	"github.com/decred/dcrlnd/kvdb"
)

var (
	hexStr    = migtest.Hex
	mockNow   = time.Unix(1711465236, 0)
	wantNewTs = mockNow.Add(-pastOffsetFix)

	pub01 = hexStr("032195e05c87bf05b4fe34d7f2dd629390460e44fceab1f48252878d79dafa5779")
	pub02 = hexStr("027e8c63c6eb86cd3bf64f58d6a86539bab58a1c25a9bc8c9e01e0a938e7b19821")
	pub03 = hexStr("020ed7807b8e384df0cdaa3ef89f71e0ffc68a67df69c7674d24fe2896ff7ee8da")

	pastTime           = string(byteOrder.AppendUint64(nil, uint64(time.Unix(1518112800, 0).UnixNano())))
	futureTime         = string(byteOrder.AppendUint64(nil, uint64(mockNow.Add(time.Second).UnixNano())))
	futureTimeAfterMig = string(byteOrder.AppendUint64(nil, uint64(wantNewTs.UnixNano())))
	peersPreMigration  = map[string]interface{}{
		pub01: map[string]interface{}{
			string(peerLastGossipMsgTSKey): pastTime,
		},
		pub02: map[string]interface{}{
			string(peerLastGossipMsgTSKey): futureTime,
		},
		pub03: map[string]interface{}{}, // Peer without timestamp
	}

	peersPostMigration = map[string]interface{}{
		pub01: map[string]interface{}{
			string(peerLastGossipMsgTSKey): pastTime,
		},
		pub02: map[string]interface{}{
			string(peerLastGossipMsgTSKey): futureTimeAfterMig,
		},
		pub03: map[string]interface{}{}, // Peer without timestamp
	}
)

func TestRemoveFutureTimestamps(t *testing.T) {
	// Modify now() to return a mock date.
	nowFunc = func() time.Time { return mockNow }

	// Prime the database with peer data in the past and future.
	before := func(tx kvdb.RwTx) error {
		return migtest.RestoreDB(tx, peersBucket, peersPreMigration)
	}

	// Double check the future dates were migrated to the past.
	after := func(tx kvdb.RwTx) error {
		return migtest.VerifyDB(tx, peersBucket, peersPostMigration)
	}

	migtest.ApplyMigration(t, before, after, RemoveFutureTimestampFromPeers, false)
}
