package migration_01_to_11

import (
	"testing"

	"github.com/decred/dcrlnd/kvdb"
)

// TestSaneNextKeyFamilyIndex tests that the generation of key family indices
// is sane and follows the propper semantics.
func TestSaneNextKeyFamilyIndex(t *testing.T) {
	t.Parallel()

	db, cleanUp, err := makeTestDB()
	defer cleanUp()
	if err != nil {
		t.Fatalf("unable to make test db: %v", err)
	}

	// Two consecutive calls should generate different indices for the same
	// key family.
	i1, err := db.NextKeyFamilyIndex(0)
	if err != nil {
		t.Fatalf("unable to generate first index: %v", err)
	}

	if i1 != 0 {
		t.Fatalf("the first returned index should be 0")
	}

	i2, err := db.NextKeyFamilyIndex(0)
	if err != nil {
		t.Fatalf("unable to generate second index: %v", err)
	}
	if i2 != 1 {
		t.Fatalf("the second returned index should be 1")
	}

	// Generating for a new key family should return the first index again.
	i3, err := db.NextKeyFamilyIndex(1)
	if err != nil {
		t.Fatalf("unable to generate index for second family: %v", err)
	}
	if i3 != 0 {
		t.Fatalf("the index for the second family should be 0")
	}

	// Trying to generate an index for an invalid family should return an
	// error.
	_, err = db.NextKeyFamilyIndex(0x80000000)
	if err != errInvalidKeyFamily {
		t.Fatalf("invalid family should return correct error; got %v",
			err)
	}

	// Manually change the db to simulate exhausting a key family
	err = kvdb.Update(db, func(tx kvdb.RwTx) error {
		keychain, err := tx.CreateTopLevelBucket(keychainBucket)
		if err != nil {
			return err
		}

		keyFamilies, err := keychain.CreateBucketIfNotExists(
			keyFamilyIndexesBucket,
		)
		if err != nil {
			return err
		}

		// Write the maximum usable index to the keyFamily 0
		var k [4]byte
		var v [4]byte
		byteOrder.PutUint32(v[:], 0x7fffffff)
		keyFamilies.Put(k[:], v[:])
		return nil
	}, func() {})
	if err != nil {
		t.Fatalf("unable to manipulate db: %v", err)
	}

	// Try to generate the next index. It should fail.
	_, err = db.NextKeyFamilyIndex(0)
	if err != errKeyFamilyExhausted {
		t.Fatalf("exhausted family should return correct error; got %v",
			err)
	}
}

// TestAtomicAccountID tests if the comparison of account IDs work as expected.
func TestAtomicAccountID(t *testing.T) {
	db, cleanUp, err := makeTestDB()
	defer cleanUp()
	if err != nil {
		t.Fatalf("unable to make test db: %v", err)
	}

	v := make([]byte, 32)
	err = db.CompareAndStoreAccountID(v)
	if err != nil {
		t.Fatalf("expected first comparison to not error. got %v", err)
	}

	err = db.CompareAndStoreAccountID(v)
	if err != nil {
		t.Fatalf("expected comparison to correct value to not error. got %v", err)
	}

	v[0] = 1
	err = db.CompareAndStoreAccountID(v)
	if err != errDifferentAccountID {
		t.Fatalf("wrong error. want '%v' got '%v'", errDifferentAccountID, err)
	}
}
