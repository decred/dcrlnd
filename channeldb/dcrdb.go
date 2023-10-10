package channeldb

import "github.com/decred/dcrlnd/kvdb"

// initDcrlndFeatures initializes features that are specific to dcrlnd.
func (db *DB) initDcrlndFeatures() error {
	return kvdb.Update(db, func(tx kvdb.RwTx) error {
		// If the inflight payments index bucket doesn't exist,
		// initialize it.
		indexBucket := tx.ReadWriteBucket(paymentsInflightIndexBucket)
		if indexBucket == nil {
			return recreatePaymentsInflightIndex(tx)
		}

		return nil
	}, func() {})
}
