package keychain

import (
	"math/rand"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/decred/dcrd/dcrec/secp256k1"
)

// versionZeroKeyFamilies is a slice of all the known key families for first
// version of the key derivation schema defined in this package.
var versionZeroKeyFamilies = []KeyFamily{
	KeyFamilyMultiSig,
	KeyFamilyRevocationBase,
	KeyFamilyHtlcBase,
	KeyFamilyPaymentBase,
	KeyFamilyDelayBase,
	KeyFamilyRevocationRoot,
	KeyFamilyNodeKey,
}

func assertEqualKeyLocator(t *testing.T, a, b KeyLocator) {
	t.Helper()
	if a != b {
		t.Fatalf("mismatched key locators: expected %v, "+
			"got %v", spew.Sdump(a), spew.Sdump(b))
	}
}

// KeyRingConstructor is a function signature that's used as a generic
// constructor for various implementations of the KeyRing interface. A string
// naming the returned interface, a function closure that cleans up any
// resources, and the clean up interface itself are to be returned.
type KeyRingConstructor func() (string, func(), KeyRing, error)

// CheckKeyRingImpl tests that the provided KeyRing implementation properly
// adheres to the expected behavior of the set of interfaces.
func CheckKeyRingImpl(t *testing.T, constructor KeyRingConstructor) {
	const numKeysToDerive = 10

	// For each implementation constructor, we'll execute an identical set
	// of tests in order to ensure that the interface adheres to our
	// nominal specification.
	keyRingName, cleanUp, keyRing, err := constructor()
	if err != nil {
		t.Fatalf("unable to create key ring %v: %v", keyRingName,
			err)
	}
	defer cleanUp()

	// First, we'll ensure that we're able to derive keys from each
	// of the known key families.
	for _, keyFam := range versionZeroKeyFamilies {
		// First, we'll ensure that we can derive the
		// *next* key in the keychain.
		keyDesc, err := keyRing.DeriveNextKey(keyFam)
		if err != nil {
			t.Fatalf("unable to derive next for "+
				"keyFam=%v: %v", keyFam, err)
		}
		assertEqualKeyLocator(t,
			KeyLocator{
				Family: keyFam,
				Index:  0,
			}, keyDesc.KeyLocator,
		)

		// We'll generate the next key and ensure it's
		// different than the first one.
		keyDescNext, err := keyRing.DeriveNextKey(keyFam)
		if err != nil {
			t.Fatalf("unable to derive next for"+
				"keyFam=%v: %v", keyFam, err)
		}
		if keyDescNext.PubKey.IsEqual(keyDesc.PubKey) {
			t.Fatal("keyring derived two " +
				"identical consecutive keys")
		}

		// We'll now re-derive that key to ensure that
		// we're able to properly access the key via
		// the random access derivation methods.
		keyLoc := KeyLocator{
			Family: keyFam,
			Index:  0,
		}
		firstKeyDesc, err := keyRing.DeriveKey(keyLoc)
		if err != nil {
			t.Fatalf("unable to derive first key for "+
				"keyFam=%v: %v", keyFam, err)
		}
		if !keyDesc.PubKey.IsEqual(firstKeyDesc.PubKey) {
			t.Fatalf("mismatched keys: expected %x, "+
				"got %x",
				keyDesc.PubKey.SerializeCompressed(),
				firstKeyDesc.PubKey.SerializeCompressed())
		}
		assertEqualKeyLocator(t,
			KeyLocator{
				Family: keyFam,
				Index:  0,
			}, firstKeyDesc.KeyLocator,
		)

		// If we now try to manually derive the next 10
		// keys (including the original key), then we
		// should get an identical public key back and
		// their KeyLocator information
		// should be set properly.
		for i := 0; i < numKeysToDerive+1; i++ {
			keyLoc := KeyLocator{
				Family: keyFam,
				Index:  uint32(i),
			}
			keyDesc, err := keyRing.DeriveKey(keyLoc)
			if err != nil {
				t.Fatalf("unable to derive first key for "+
					"keyFam=%v: %v", keyFam, err)
			}

			// Ensure that the key locator matches
			// up as well.
			assertEqualKeyLocator(
				t, keyLoc, keyDesc.KeyLocator,
			)
		}

		// If this succeeds, then we'll also try to
		// derive a random index within the range.
		randKeyIndex := uint32(rand.Int31())
		keyLoc = KeyLocator{
			Family: keyFam,
			Index:  randKeyIndex,
		}
		keyDesc, err = keyRing.DeriveKey(keyLoc)
		if err != nil {
			t.Fatalf("unable to derive key_index=%v "+
				"for keyFam=%v: %v",
				randKeyIndex, keyFam, err)
		}
		assertEqualKeyLocator(
			t, keyLoc, keyDesc.KeyLocator,
		)
	}

}

// SecretKeyRingConstructor is a function signature that's used as a generic
// constructor for various implementations of the SecretKeyRing interface. A
// string naming the returned interface, a function closure that cleans up any
// resources, and the clean up interface itself are to be returned.
type SecretKeyRingConstructor func() (string, func(), SecretKeyRing, error)

// TestSecretKeyRingDerivation tests that each known SecretKeyRing
// implementation properly adheres to the expected behavior of the set of
// interface.
func CheckSecretKeyRingImpl(t *testing.T, constructor SecretKeyRingConstructor) {

	// For each implementation constructor, we'll execute an identical set
	// of tests in order to ensure that the interface adheres to our
	// nominal specification.
	keyRingName, cleanUp, secretKeyRing, err := constructor()
	if err != nil {
		t.Fatalf("unable to create secret key ring %v: %v",
			keyRingName, err)
	}
	defer cleanUp()

	// For, each key family, we'll ensure that we're able to obtain
	// the private key of a randomly select child index within the
	// key family.
	for _, keyFam := range versionZeroKeyFamilies {
		randKeyIndex := uint32(rand.Int31())
		keyLoc := KeyLocator{
			Family: keyFam,
			Index:  randKeyIndex,
		}

		// First, we'll query for the public key for
		// this target key locator.
		pubKeyDesc, err := secretKeyRing.DeriveKey(keyLoc)
		if err != nil {
			t.Fatalf("unable to derive pubkey "+
				"(fam=%v, index=%v): %v",
				keyLoc.Family,
				keyLoc.Index, err)
		}

		// With the public key derive, ensure that
		// we're able to obtain the corresponding
		// private key correctly.
		privKey, err := secretKeyRing.DerivePrivKey(KeyDescriptor{
			KeyLocator: keyLoc,
		})
		if err != nil {
			t.Fatalf("unable to derive priv "+
				"(fam=%v, index=%v): %v", keyLoc.Family,
				keyLoc.Index, err)
		}

		// Finally, ensure that the keys match up
		// properly.
		if !pubKeyDesc.PubKey.IsEqual(privKey.PubKey()) {
			t.Fatalf("pubkeys mismatched: expected %x, got %x",
				pubKeyDesc.PubKey.SerializeCompressed(),
				privKey.PubKey().SerializeCompressed())
		}

		// Next, we'll test that we're able to derive a
		// key given only the public key and key
		// family.
		//
		// Derive a new key from the key ring.
		keyDesc, err := secretKeyRing.DeriveNextKey(keyFam)
		if err != nil {
			t.Fatalf("unable to derive key: %v", err)
		}

		// We'll now construct a key descriptor that
		// requires us to scan the key range, and query
		// for the key, we should be able to find it as
		// it's valid.
		keyDesc = KeyDescriptor{
			PubKey: keyDesc.PubKey,
			KeyLocator: KeyLocator{
				Family: keyFam,
			},
		}
		privKey, err = secretKeyRing.DerivePrivKey(keyDesc)
		if err != nil {
			t.Fatalf("unable to derive priv key "+
				"via scanning: %v", err)
		}

		// Having to resort to scanning, we should be
		// able to find the target public key.
		if !keyDesc.PubKey.IsEqual(privKey.PubKey()) {
			t.Fatalf("pubkeys mismatched: expected %x, got %x",
				pubKeyDesc.PubKey.SerializeCompressed(),
				privKey.PubKey().SerializeCompressed())
		}

		// We'll try again, but this time with an
		// unknown public key.
		var empty [32]byte
		_, pub := secp256k1.PrivKeyFromBytes(empty[:])
		keyDesc.PubKey = pub

		// If we attempt to query for this key, then we
		// should get ErrCannotDerivePrivKey.
		_, err = secretKeyRing.DerivePrivKey(
			keyDesc,
		)
		if err != ErrCannotDerivePrivKey {
			t.Fatalf("expected %T, instead got %v",
				ErrCannotDerivePrivKey, err)
		}

		// TODO(roasbeef): scalar mult once integrated
	}
}
