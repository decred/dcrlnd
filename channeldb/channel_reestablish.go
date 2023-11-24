package channeldb

import (
	"time"

	"github.com/btcsuite/btcwallet/walletdb"
	"github.com/decred/dcrlnd/kvdb"
	"github.com/decred/dcrlnd/lnwire"
)

var (
	// chanReestablishWaitTimeBucket is a bucket that stores the total
	// elapsed time that has been spent waiting for a channel reestablish
	// message while a peer has been online.
	//
	// Keys are ShortChannelID, values are time.Duration.
	chanReestablishWaitTimeBucket = []byte("chan-reestablish-wait-time")
)

// shortChanIDToBytes encodes a short channel id as a byte slice.
func shortChanIDToBytes(s lnwire.ShortChannelID) []byte {
	var b [8]byte
	byteOrder.PutUint64(b[:], s.ToUint64())
	return b[:]
}

// readChanReestablishWaitTime decodes a value from the
// chanReestablishWaitTimeBucket bucket.
func readChanReestablishWaitTime(v []byte) time.Duration {
	if len(v) < 8 {
		return 0
	}
	return time.Duration(byteOrder.Uint64(v))
}

// putChanReestablishWaitTime stores a value in the chanReestablishWaitTimeBucket.
func putChanReestablishWaitTime(bucket walletdb.ReadWriteBucket, key []byte, v time.Duration) error {
	var b [8]byte
	byteOrder.PutUint64(b[:], uint64(v))
	return bucket.Put(key, b[:])
}

// AddToChanReestablishWaitTime adds the passed waitTime interval to the
// total time that a peer has been online without reestablishing the passed
// channel.
func (d *DB) AddToChanReestablishWaitTime(chanID lnwire.ShortChannelID, waitTime time.Duration) error {
	return kvdb.Update(d, func(tx kvdb.RwTx) error {
		bucket := tx.ReadWriteBucket(chanReestablishWaitTimeBucket)
		if bucket == nil {
			var err error
			bucket, err = tx.CreateTopLevelBucket(chanReestablishWaitTimeBucket)
			if err != nil {
				return err
			}
		}

		// Read the existing wait time from the DB.
		key := shortChanIDToBytes(chanID)

		// Store current+additional wait time.
		current := readChanReestablishWaitTime(bucket.Get(key))
		newWaitTime := current + waitTime
		return putChanReestablishWaitTime(bucket, key, newWaitTime)
	}, func() {})
}

// ResetChanReestablishWaitTime zeros the time that a peer has spent online
// while not reestablishing a channel.  This is called when a channel has been
// successfully reestablished.
func (d *DB) ResetChanReestablishWaitTime(chanID lnwire.ShortChannelID) error {
	return kvdb.Update(d, func(tx kvdb.RwTx) error {
		var err error
		bucket := tx.ReadWriteBucket(chanReestablishWaitTimeBucket)
		if bucket == nil {
			bucket, err = tx.CreateTopLevelBucket(chanReestablishWaitTimeBucket)
			if err != nil {
				return err
			}
		}

		key := shortChanIDToBytes(chanID)
		return putChanReestablishWaitTime(bucket, key, 0)
	}, func() {})
}

// GetChanReestablishWaitTime returns the total time (across all connections)
// that a peer has been online while NOT reestablishing a channel.
func (d *DB) GetChanReestablishWaitTime(chanID lnwire.ShortChannelID) (waitTime time.Duration, err error) {
	err = kvdb.View(d, func(tx kvdb.RTx) error {
		bucket := tx.ReadBucket(chanReestablishWaitTimeBucket)
		if bucket == nil {
			return nil
		}

		key := shortChanIDToBytes(chanID)
		waitTime = readChanReestablishWaitTime(bucket.Get(key))
		return nil
	}, func() {})
	return
}
