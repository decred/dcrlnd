package keychain

import (
	"testing"

	"github.com/decred/dcrd/chaincfg"
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/hdkeychain"
)

var (
	testHDSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x98, 0xa3, 0xef, 0xb9,
		0x69, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}
)

// createTestHDKeyRing creates a test HDKeyRing implementation that works
// purely in memory, using the default test HD seed as root master private key.
func createTestHDKeyRing() *HDKeyRing {
	// We can ignore errors here because they could only be for invalid
	// keys/seeds and the default test seed does not produce errors.
	master, _ := hdkeychain.NewMaster(testHDSeed[:], &chaincfg.RegNetParams)
	masterPubs := make(map[KeyFamily]*hdkeychain.ExtendedKey,
		len(versionZeroKeyFamilies))
	indexes := make(map[KeyFamily]uint32, len(versionZeroKeyFamilies))
	for _, keyFam := range versionZeroKeyFamilies {
		masterPubs[keyFam], _ = master.Child(uint32(keyFam))
		indexes[keyFam] = 0
	}

	fetchMasterPriv := func(keyFam KeyFamily) (*hdkeychain.ExtendedKey, error) {
		// We never neutered the keys in masterPubs, so they also
		// contain the private key.
		return masterPubs[keyFam], nil
	}

	nextIndex := func(keyFam KeyFamily) (uint32, error) {
		index := indexes[keyFam]
		indexes[keyFam] += 1
		return index, nil
	}

	return NewHDKeyRing(masterPubs, fetchMasterPriv, nextIndex)
}

// TestHDKeyRingImpl tests whether the HDKeyRing implementation conforms to the
// required interface spec.
func TestHDKeyRingImpl(t *testing.T) {
	t.Parallel()

	CheckKeyRingImpl(t,
		func() (string, func(), KeyRing, error) {
			return "hdkeyring", func() {}, createTestHDKeyRing(), nil
		},
	)
}

// TestHDSecretKeyRingImpl tests whether the HDKeyRing implementation conforms
// to the required interface spec.
func TestHDSecretKeyRingImpl(t *testing.T) {
	t.Parallel()

	CheckSecretKeyRingImpl(t,
		func() (string, func(), SecretKeyRing, error) {
			return "hdkeyring", func() {}, createTestHDKeyRing(), nil
		},
	)
}

func init() {
	// We'll clamp the max range scan to constrain the run time of the
	// private key scan test.
	MaxKeyRangeScan = 3
}
