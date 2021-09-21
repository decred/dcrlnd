package channeldb

import (
	dcrmigration01 "github.com/decred/dcrlnd/channeldb/dcrmigrations/migration01"
	"github.com/decred/dcrlnd/kvdb"
)

var (
	dcrMetaBucket = []byte("dcrlnd_metadata")
)

var (
	// dcrDbVersions are the decred-only migrations.
	dcrDbVersions = []version{
		{number: 1, migration: dcrmigration01.FixMigration20},
	}
)

// latestDcrDbVersion returns the latest version of the decred-specific db
// migrations.
func latestDcrDbVersion() uint32 {
	return dcrDbVersions[len(dcrDbVersions)-1].number
}

// applyDecredMigations applies the decred-only migrations.
func (d *DB) applyDecredMigations(tx kvdb.RwTx, dbVersion uint32) error {
	latestVersion := dcrDbVersions[len(dcrDbVersions)-1].number
	log.Infof("Checking for decred-specicic migrations latest_version=%d, "+
		"db_version=%d", latestVersion, dbVersion)

	if latestVersion < dbVersion {
		log.Errorf("Refusing to revert from decred db_version=%d to "+
			"lower version=%d", dbVersion, latestVersion)
		return ErrDBReversion
	}

	if latestVersion == dbVersion {
		// Nothing to do.
		return nil
	}

	log.Infof("Performing decred-specific database schema migration")

	metaBucket := tx.ReadWriteBucket(dcrMetaBucket)
	for _, mig := range dcrDbVersions {
		if mig.migration == nil {
			continue
		}
		if mig.number <= dbVersion {
			continue
		}

		log.Infof("Applying migration #%d", mig.number)

		if err := mig.migration(tx); err != nil {
			log.Infof("Unable to apply migration #%d",
				mig.number)
			return err
		}

		// Save the new db version.
		dbVersion = mig.number
		err := metaBucket.Put(dbVersionKey, byteOrder.AppendUint32(nil, dbVersion))
		if err != nil {
			return err
		}
	}

	// Stop if running in dry-run mode.
	if d.dryRun {
		return ErrDryRunMigrationOK
	}

	return nil
}

// syncDcrlndDBVersions performs the dcrlnd-specific db upgrades.
func (d *DB) syncDcrlndDBVersions(tx kvdb.RwTx) error {
	// Read dcr-specific version.
	var dbVersion uint32
	bucket := tx.ReadWriteBucket(dcrMetaBucket)
	if bucket == nil {
		// Filled meta bucket but empty dcr-specific meta bucket.
		// Create dcr one.
		var err error
		bucket, err = tx.CreateTopLevelBucket(dcrMetaBucket)
		if err != nil {
			return err
		}

		// If the global meta bucket is empty, it's a new db.
		if tx.ReadBucket(metaBucket) == nil {
			dbVersion = latestDcrDbVersion()
		}

		// dbVersion == 0.
		bucket.Put(dbVersionKey, byteOrder.AppendUint32(nil, dbVersion))
	} else {
		v := bucket.Get(dbVersionKey)
		if v != nil {
			dbVersion = byteOrder.Uint32(v)
		}
	}

	// Apply dcr-specific migrations.
	return d.applyDecredMigations(tx, dbVersion)
}

// initDcrlndFeatures initializes features that are specific to dcrlnd.
func (d *DB) initDcrlndFeatures() error {
	return kvdb.Update(d, func(tx kvdb.RwTx) error {
		if err := d.syncDcrlndDBVersions(tx); err != nil {
			return err
		}

		// If the inflight payments index bucket doesn't exist,
		// initialize it.
		indexBucket := tx.ReadWriteBucket(paymentsInflightIndexBucket)
		if indexBucket == nil {
			return recreatePaymentsInflightIndex(tx)
		}

		return nil
	}, func() {})
}

// LocalOpenChanIDs returns a map of channel IDs of all open channels in the
// local DB.
func (d *DB) LocalOpenChanIDs() (map[uint64]struct{}, error) {
	// Note: this is less efficient than it could be, because it iterates
	// through the entire list of channels and then discards all that just
	// to extract the channel id. In the future, decode that field directly.
	openChans, err := d.ChannelStateDB().FetchAllOpenChannels()
	if err != nil {
		return nil, err
	}

	res := make(map[uint64]struct{}, len(openChans))
	for _, c := range openChans {
		res[c.ShortChanID().ToUint64()] = struct{}{}
	}
	return res, nil
}
