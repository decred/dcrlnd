package keychain

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/hdkeychain/v3"
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

		pubkey, err := secp256k1.ParsePubKey(indexKey.SerializedPubKey())
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
	if masterPub == nil {
		return KeyDescriptor{}, fmt.Errorf("masterpub for keyfamily %d does not exist", keyLoc.Family)
	}
	key, err := masterPub.Child(keyLoc.Index)
	if err != nil {
		return KeyDescriptor{}, err
	}
	pubKey, err := secp256k1.ParsePubKey(key.SerializedPubKey())
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
		exPrivKey, err := masterPriv.Child(keyDesc.Index)
		if err != nil {
			return nil, err
		}
		serPrivKey, err := exPrivKey.SerializedPrivKey()
		if err != nil {
			return nil, err
		}
		return secp256k1.PrivKeyFromBytes(serPrivKey), nil
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

		pubKey, err := secp256k1.ParsePubKey(privKey.SerializedPubKey())
		if err != nil {
			// simply skip invalid keys here
			continue
		}

		if keyDesc.PubKey.IsEqual(pubKey) {
			serPriv, err := privKey.SerializedPrivKey()
			if err != nil {
				return nil, err
			}
			return secp256k1.PrivKeyFromBytes(serPriv), nil
		}
	}

	return nil, ErrCannotDerivePrivKey
}

// ECDH performs a scalar multiplication (ECDH-like operation) between the
// target key descriptor and remote public key. The output returned will be the
// sha256 of the resulting shared point serialized in compressed format. If k
// is our private key, and P is the public key, we perform the following
// operation:
//
//	sx := k*P
//	 s := sha256(sx.SerializeCompressed())
//
// NOTE: This is part of the keychain.ECDHRing interface.
func (kr *HDKeyRing) ECDH(keyDesc KeyDescriptor,
	pub *secp256k1.PublicKey) ([32]byte, error) {

	privKey, err := kr.DerivePrivKey(keyDesc)
	if err != nil {
		return [32]byte{}, err

	}

	// Privkey to ModNScalar.
	var privKeyModn secp256k1.ModNScalar
	privKeyModn.SetByteSlice(privKey.Serialize())

	// Pubkey to JacobianPoint.
	var pubJacobian, res secp256k1.JacobianPoint
	pub.AsJacobian(&pubJacobian)

	// Calculate shared point and ensure it's on the curve.
	secp256k1.ScalarMultNonConst(&privKeyModn, &pubJacobian, &res)
	res.ToAffine()
	sharedPub := secp256k1.NewPublicKey(&res.X, &res.Y)
	if !sharedPub.IsOnCurve() {
		return [32]byte{}, fmt.Errorf("Derived ECDH point is not on the secp256k1 curve")
	}

	// Hash of the serialized point is the shared secret.
	h := sha256.Sum256(sharedPub.SerializeCompressed())

	return h, nil

}

// SignMessage signs the given message, single or double chainhashsing it
// first, with the private key described in the key descriptor.
//
// NOTE: This is part of the keychain.DigestSignerRing interface.
func (kr *HDKeyRing) SignMessage(keyDesc KeyDescriptor,
	msg []byte, doubleHash bool) (*ecdsa.Signature, error) {

	privKey, err := kr.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err

	}

	var digest []byte
	if doubleHash {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.Sign(privKey, digest), nil
}

// SignMessageCompact signs the given message, single or double chainhash hashing
// it first, with the private key described in the key descriptor and returns
// the signature in the compact, public key recoverable format.
//
// NOTE: This is part of the keychain.DigestSignerRing interface.
func (kr *HDKeyRing) SignMessageCompact(keyDesc KeyDescriptor,
	msg []byte, doubleHash bool) ([]byte, error) {

	privKey, err := kr.DerivePrivKey(keyDesc)
	if err != nil {
		return nil, err

	}

	var digest []byte
	if doubleHash {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.SignCompact(privKey, digest[:], true), nil
}
