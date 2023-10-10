package channeldb

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/btcsuite/btcwallet/walletdb"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/routing/route"
)

var (
	// peerLastGossipMsgTSKey is the key for a value in the peer bucket that
	// tracks the unix timestamp of the last received gossip message
	// (ChannelUpdate, ChannelAnnouncement, NodeAnnounce).
	peerLastGossipMsgTSKey = []byte("last-gossip-msg-ts")

	// ErrNoPeerLastGossipMsgTS is returned when there are no recorded
	// gossip msg timestamps for a peer.
	ErrNoPeerLastGossipMsgTS = errors.New("last gossip msg ts not recorded")

	// ErrOutdatedLastGossipMsgTS is returned when an attempt is made to
	// store a gossip timestamp that is before the previously stored one.
	ErrOutdatedLastGossipMsgTS = errors.New("last gossip msg ts specified " +
		"is older than previously recorded ts")
)

// readPeerLastGossipMsgTS reads and deserializes the last gossip msg timestamp
// for a given peer.
func readPeerLastGossipMsgTS(rootPeersBucket walletdb.ReadBucket, peer route.Vertex) (time.Time, error) {
	var ts time.Time

	peerBucket := rootPeersBucket.NestedReadBucket(peer[:])
	if peerBucket == nil {
		return ts, fmt.Errorf("%w for peer %v",
			ErrNoPeerLastGossipMsgTS, peer)
	}

	tsBytes := peerBucket.Get(peerLastGossipMsgTSKey)
	if tsBytes == nil {
		return ts, fmt.Errorf("%w for peer %v",
			ErrNoPeerLastGossipMsgTS, peer)
	}

	r := bytes.NewReader(tsBytes)
	return deserializeTime(r)
}

// UpdatePeerLastGossipMsgTS stores the specified timestamp as the timestamp of
// the most recent gossip message received from the specified peer. If this
// timestamp is older than a previously stored timestamp, this function returns
// an error.
func (d *DB) UpdatePeerLastGossipMsgTS(peer route.Vertex, ts time.Time) error {
	return kvdb.Update(d, func(tx kvdb.RwTx) error {
		peers := tx.ReadWriteBucket(peersBucket)

		// Read existing ts. Error is ignored here, because an empty
		// time equals 0.
		prevTS, _ := readPeerLastGossipMsgTS(peers, peer)
		if ts.Before(prevTS) {
			return fmt.Errorf("%w: %s is before %s",
				ErrOutdatedLastGossipMsgTS, ts, prevTS)
		}

		peerBucket, err := peers.CreateBucketIfNotExists(
			peer[:],
		)
		if err != nil {
			return err
		}

		var b bytes.Buffer
		err = serializeTime(&b, ts)
		if err != nil {
			return err
		}

		err = peerBucket.Put(peerLastGossipMsgTSKey, b.Bytes())
		if err != nil {
			return err
		}

		return nil
	}, func() {})
}

// ReadPeerLastGossipMsgTS returns the timestamp stored as most recent for
// a gossip message received from the specified peer.
func (d *DB) ReadPeerLastGossipMsgTS(peer route.Vertex) (time.Time, error) {
	var ts time.Time
	err := kvdb.View(d, func(tx kvdb.RTx) error {
		peers := tx.ReadBucket(peersBucket)
		var err error
		ts, err = readPeerLastGossipMsgTS(peers, peer)
		return err
	}, func() { ts = time.Time{} })

	return ts, err
}
