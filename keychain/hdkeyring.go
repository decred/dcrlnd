package keychain

import (
	"crypto/sha256"
	"errors"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/decred/dcrd/hdkeychain"
)

var errPubOnlyKeyRing = errors.New("keyring configured as pubkey only")

// HDKeyRing is an implementation of both the KeyRing and SecretKeyRing
// interfaces backed by a root master key pair. The master extended public keys
// (one for each required key family) is maintained in memory at all times,
// while the extended private key must be produred by a function (specified
// during struct setup) whenever requested.
type HDKeyRing struct {
	masterPubs      map[KeyFamily]*hdkeychain.ExtendedKey
	fetchMasterPriv func(KeyFamily) (*hdkeychain.ExtendedKey, error)
	nextIndex       func(KeyFamily) (uint32, error)
}

// Compile time type assertions to ensure HDKeyRing fulfills the desired
// interfaces.
var _ KeyRing = (*HDKeyRing)(nil)
var _ SecretKeyRing = (*HDKeyRing)(nil)

// NewHDKeyRing creates a new implementation of the keychain.SecretKeyRing
// interface backed by a set of extended HD keys.
//
// The passed fetchMasterPriv must be able to return the master private key for
// the keyring in a timely fashion, otherwise sign operations may be delayed.
// If this function is not specified, then the KeyRing cannot derive private
// keys.
//
// The passed nextIndex must be able to return the next (unused) index for each
// existing KeyFamily used in ln operations. Indication that the index was
// returned should be persisted in some way, such that public key reuse is
// minimized and the same index is not returned twice. In other words, calling
// nextAddrIndex twice should return different values, otherwise key derivation
// might hang forever.
//
// If either the set of master public keys (one for each family) or the next
// address index are not provided, the results from trying to use this function
// are undefined.
func NewHDKeyRing(masterPubs map[KeyFamily]*hdkeychain.ExtendedKey,
	fetchMasterPriv func(KeyFamily) (*hdkeychain.ExtendedKey, error),
	nextIndex func(KeyFamily) (uint32, error)) *HDKeyRing {

	return &HDKeyRing{
		masterPubs:      masterPubs,
		fetchMasterPriv: fetchMasterPriv,
		nextIndex:       nextIndex,
	}
}

// DeriveNextKey attempts to derive the *next* key within the key family
// (account in BIP43) specified. This method should return the next external
// child within this branch.
//
// NOTE: This is part of the keychain.KeyRing interface.
func (kr *HDKeyRing) DeriveNextKey(keyFam KeyFamily) (KeyDescriptor, error) {

	masterPub := kr.masterPubs[keyFam]

	for {
		// Derive the key and skip to next if invalid.
		index, err := kr.nextIndex(keyFam)
		if err != nil {
			return KeyDescriptor{}, err
		}
		indexKey, err := masterPub.Child(index)

		if err == hdkeychain.ErrInvalidChild {
			continue
		}
		if err != nil {
			return KeyDescriptor{}, err
		}

		pubkey, err := indexKey.ECPubKey()
		if err != nil {
			return KeyDescriptor{}, err
		}

		return KeyDescriptor{
			PubKey: pubkey,
			KeyLocator: KeyLocator{
				Family: keyFam,
				Index:  index,
			},
		}, nil
	}
}

// DeriveKey attempts to derive an arbitrary key specified by the passed
// KeyLocator. This may be used in several recovery scenarios, or when manually
// rotating something like our current default node key.
//
// NOTE: This is part of the keychain.KeyRing interface.
func (kr HDKeyRing) DeriveKey(keyLoc KeyLocator) (KeyDescriptor, error) {
	masterPub := kr.masterPubs[keyLoc.Family]
	key, err := masterPub.Child(keyLoc.Index)
	if err != nil {
		return KeyDescriptor{}, err
	}
	pubKey, err := key.ECPubKey()
	if err != nil {
		return KeyDescriptor{}, err
	}

	return KeyDescriptor{
		KeyLocator: keyLoc,
		PubKey:     pubKey,
	}, nil
}

// DerivePrivKey attempts to derive the private key that corresponds to the
// passed key descriptor.
//
// NOTE: This is part of the keychain.SecretKeyRing interface.
func (kr *HDKeyRing) DerivePrivKey(keyDesc KeyDescriptor) (*secp256k1.PrivateKey, error) {

	if kr.fetchMasterPriv == nil {
		return nil, errPubOnlyKeyRing
	}

	// We'll grab the master pub key for the provided account (family) then
	// manually derive the addresses here.
	masterPriv, err := kr.fetchMasterPriv(keyDesc.Family)
	if err != nil {
		return nil, err
	}

	// If the public key isn't set or they have a non-zero index,
	// then we know that the caller instead knows the derivation
	// path for a key.
	if keyDesc.PubKey == nil || keyDesc.Index > 0 {
		privKey, err := masterPriv.Child(keyDesc.Index)
		if err != nil {
			return nil, err
		}
		return privKey.ECPrivKey()
	}

	// If the public key isn't nil, then this indicates that we
	// need to scan for the private key, assuming that we know the
	// valid key family.
	for i := 0; i < MaxKeyRangeScan; i++ {
		// Derive the next key in the range and fetch its
		// managed address.
		privKey, err := masterPriv.Child(uint32(i))
		if err == hdkeychain.ErrInvalidChild {
			continue
		}

		if err != nil {
			return nil, err
		}

		pubKey, err := privKey.ECPubKey()
		if err != nil {
			// simply skip invalid keys here
			continue
		}

		if keyDesc.PubKey.IsEqual(pubKey) {
			return privKey.ECPrivKey()
		}
	}

	return nil, ErrCannotDerivePrivKey
}

// ScalarMult performs a scalar multiplication (ECDH-like operation) between
// the target key descriptor and remote public key. The output returned will be
// the sha256 of the resulting shared point serialized in compressed format. If
// k is our private key, and P is the public key, we perform the following
// operation:
//
//  sx := k*P s := sha256(sx.SerializeCompressed())
//
// NOTE: This is part of the keychain.SecretKeyRing interface.
func (kr *HDKeyRing) ScalarMult(keyDesc KeyDescriptor,
	pub *secp256k1.PublicKey) ([]byte, error) {

	privKey, err := kr.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err
	}

	s := &secp256k1.PublicKey{}
	x, y := secp256k1.S256().ScalarMult(pub.X, pub.Y, privKey.D.Bytes())
	s.X = x
	s.Y = y

	h := sha256.Sum256(s.SerializeCompressed())

	return h[:], nil
}
