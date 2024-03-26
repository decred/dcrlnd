package dcrmigration02

import (
	"bytes"
	"encoding/binary"
	"io"
	"time"

	"github.com/decred/dcrlnd/kvdb"
)

const (
	// pastOffsetFix is how much to rewind the date of the last gossip
	// timestamp from now.
	pastOffsetFix = time.Hour * 72
)

var (
	// peersBucket is the key for the bucket that holds per-gossip-peer
	// info.
	peersBucket = []byte("peers-bucket")

	// peerLastGossipMsgTSKey is the key for a value in the peer bucket that
	// tracks the unix timestamp of the last received gossip message
	// (ChannelUpdate, ChannelAnnouncement, NodeAnnounce).
	peerLastGossipMsgTSKey = []byte("last-gossip-msg-ts")

	// Big endian is the preferred byte order, due to cursor scans over
	// integer keys iterating in order.
	byteOrder = binary.BigEndian

	// nowFunc is modified during tests to ensure a consistent now.
	nowFunc = time.Now
)

// deserializeTime deserializes time as unix nanoseconds.
func deserializeTime(r io.Reader) (time.Time, error) {
	var scratch [8]byte
	if _, err := io.ReadFull(r, scratch[:]); err != nil {
		return time.Time{}, err
	}

	// Convert to time.Time. Interpret unix nano time zero as a zero
	// time.Time value.
	unixNano := byteOrder.Uint64(scratch[:])
	if unixNano == 0 {
		return time.Time{}, nil
	}

	return time.Unix(0, int64(unixNano)), nil
}

// serializeTime serializes time as unix nanoseconds.
func serializeTime(w io.Writer, t time.Time) error {
	var scratch [8]byte

	// Convert to unix nano seconds, but only if time is non-zero. Calling
	// UnixNano() on a zero time yields an undefined result.
	var unixNano int64
	if !t.IsZero() {
		unixNano = t.UnixNano()
	}

	byteOrder.PutUint64(scratch[:], uint64(unixNano))
	_, err := w.Write(scratch[:])
	return err
}

// RemoveFutureTimestampFromPeers fixes a bug that caused the db to store
// a future date as the latest timestamp for gossip messages, causing the peer
// to fail to request gossip updates.
func RemoveFutureTimestampFromPeers(tx kvdb.RwTx) error {
	// If the timestamp is in the future, set it as 72h in the past to
	// ensure we get any new channel updates.
	now := nowFunc()
	newTs := now.Add(-pastOffsetFix)

	var b bytes.Buffer
	err := serializeTime(&b, newTs)
	if err != nil {
		return err
	}

	// Get the target bucket.
	var migrated int
	bucket := tx.ReadWriteBucket(peersBucket)
	err = bucket.ForEach(func(k, v []byte) error {
		peerBucket := bucket.NestedReadWriteBucket(k)
		if peerBucket == nil {
			return nil
		}

		tsBytes := peerBucket.Get(peerLastGossipMsgTSKey)
		if tsBytes == nil {
			return nil
		}

		ts, err := deserializeTime(bytes.NewReader(tsBytes))
		if err != nil {
			return nil
		}

		if !ts.After(now) {
			return nil
		}

		// Entry needs to be migrated.
		migrated++
		return peerBucket.Put(peerLastGossipMsgTSKey, b.Bytes())
	})

	if err != nil {
		return err
	}

	log.Infof("Migrated %d future timestamps to %s", migrated,
		newTs.Format(time.RFC3339))
	return nil
}
