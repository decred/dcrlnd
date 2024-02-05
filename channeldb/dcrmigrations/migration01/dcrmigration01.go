package dcrmigration01

import (
	"bytes"
	"encoding/binary"

	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/kvdb"
)

var (
	byteOrder = binary.BigEndian

	// outpointBucket is an index mapping outpoints to a tlv
	// stream of channel data.
	outpointBucket = []byte("outpoint-bucket")
)

// FixMigration20 fixes a version of the version 20 that had a wrong
// implementation for the writeOutpoint codec function. This assumes that
// migration20 was executed and now needs to be fixed.
func FixMigration20(tx kvdb.RwTx) error {
	// Get the target bucket.
	bucket := tx.ReadWriteBucket(outpointBucket)

	// Collect the data that needs migration.
	var keys []*wire.OutPoint
	values := map[*wire.OutPoint][]byte{}
	err := bucket.ForEach(func(k, v []byte) error {
		op := new(wire.OutPoint)
		r := bytes.NewReader(k)
		if err := readMig20Outpoint(r, op); err != nil {
			return err
		}

		keys = append(keys, op)
		switch {
		case v == nil:
			values[op] = nil
		case len(v) == 0:
			values[op] = []byte{}
		default:
			values[op] = append([]byte(nil), v...)
		}

		return nil
	})
	if err != nil {
		return err
	}

	log.Infof("Migrating %d entries", len(keys))

	for _, op := range keys {
		log.Debugf("Migrating outpoint %s", op)

		var oldOpBuf bytes.Buffer
		if err := writeMig20Outpoint(&oldOpBuf, op); err != nil {
			return err
		}

		if err := bucket.Delete(oldOpBuf.Bytes()); err != nil {
			return err
		}

		var newOpBuf bytes.Buffer
		if err := writeOkOutpoint(&newOpBuf, op); err != nil {
			return err
		}
		value := values[op]
		if err := bucket.Put(newOpBuf.Bytes(), value); err != nil {
			return err
		}
	}

	return nil
}
