package mock

import (
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"

	"github.com/decred/dcrlnd/keychain"
)

// SecretKeyRing is a mock implementation of the SecretKeyRing interface.
type SecretKeyRing struct {
	RootKey *secp256k1.PrivateKey
}

// DeriveNextKey currently returns dummy values.
func (s *SecretKeyRing) DeriveNextKey(
	_ keychain.KeyFamily) (keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{
		PubKey: s.RootKey.PubKey(),
	}, nil
}

// DeriveKey currently returns dummy values.
func (s *SecretKeyRing) DeriveKey(
	_ keychain.KeyLocator) (keychain.KeyDescriptor, error) {

	return keychain.KeyDescriptor{
		PubKey: s.RootKey.PubKey(),
	}, nil
}

// DerivePrivKey currently returns dummy values.
func (s *SecretKeyRing) DerivePrivKey(
	_ keychain.KeyDescriptor) (*secp256k1.PrivateKey, error) {

	return s.RootKey, nil
}

// ECDH currently returns dummy values.
func (s *SecretKeyRing) ECDH(_ keychain.KeyDescriptor,
	_ *secp256k1.PublicKey) ([32]byte, error) {

	return [32]byte{}, nil
}

// SignMessage signs the passed message and ignores the KeyDescriptor.
func (s *SecretKeyRing) SignMessage(_ keychain.KeyLocator,
	msg []byte, doubleHash bool) (*ecdsa.Signature, error) {

	var digest []byte
	if doubleHash {
		return nil, fmt.Errorf("dcrlnd does not do doubleHash signing")
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.Sign(s.RootKey, digest), nil
}

// SignMessageCompact signs the passed message.
func (s *SecretKeyRing) SignMessageCompact(_ keychain.KeyLocator,
	msg []byte, doubleHash bool) ([]byte, error) {

	var digest []byte
	if doubleHash {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.SignCompact(s.RootKey, digest[:], true), nil
}
