package keychain

import (
	"crypto/sha256"
	"fmt"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// NewPubKeyECDH wraps the given key of the key ring so it adheres to the
// SingleKeyECDH interface.
func NewPubKeyECDH(keyDesc KeyDescriptor, ecdh ECDHRing) *PubKeyECDH {
	return &PubKeyECDH{
		keyDesc: keyDesc,
		ecdh:    ecdh,
	}
}

// PubKeyECDH is an implementation of the SingleKeyECDH interface. It wraps an
// ECDH key ring so it can perform ECDH shared key generation against a single
// abstracted away private key.
type PubKeyECDH struct {
	keyDesc KeyDescriptor
	ecdh    ECDHRing
}

// PubKey returns the public key of the private key that is abstracted away by
// the interface.
//
// NOTE: This is part of the SingleKeyECDH interface.
func (p *PubKeyECDH) PubKey() *secp256k1.PublicKey {
	return p.keyDesc.PubKey
}

// ECDH performs a scalar multiplication (ECDH-like operation) between the
// abstracted private key and a remote public key. The output returned will be
// the sha256 of the resulting shared point serialized in compressed format. If
// k is our private key, and P is the public key, we perform the following
// operation:
//
//  sx := k*P
//  s := sha256(sx.SerializeCompressed())
//
// NOTE: This is part of the SingleKeyECDH interface.
func (p *PubKeyECDH) ECDH(pubKey *secp256k1.PublicKey) ([32]byte, error) {
	return p.ecdh.ECDH(p.keyDesc, pubKey)
}

// PrivKeyECDH is an implementation of the SingleKeyECDH in which we do have the
// full private key. This can be used to wrap a temporary key to conform to the
// SingleKeyECDH interface.
type PrivKeyECDH struct {
	// PrivKey is the private key that is used for the ECDH operation.
	PrivKey *secp256k1.PrivateKey
}

// PubKey returns the public key of the private key that is abstracted away by
// the interface.
//
// NOTE: This is part of the SingleKeyECDH interface.
func (p *PrivKeyECDH) PubKey() *secp256k1.PublicKey {
	return p.PrivKey.PubKey()
}

// ECDH performs a scalar multiplication (ECDH-like operation) between the
// abstracted private key and a remote public key. The output returned will be
// the sha256 of the resulting shared point serialized in compressed format. If
// k is our private key, and P is the public key, we perform the following
// operation:
//
//  sx := k*P
//  s := sha256(sx.SerializeCompressed())
//
// NOTE: This is part of the SingleKeyECDH interface.
func (p *PrivKeyECDH) ECDH(pub *secp256k1.PublicKey) ([32]byte, error) {

	// Privkey to ModNScalar.
	var privKeyModn secp256k1.ModNScalar
	privKeyModn.SetByteSlice(p.PrivKey.Serialize())

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

var _ SingleKeyECDH = (*PubKeyECDH)(nil)
var _ SingleKeyECDH = (*PrivKeyECDH)(nil)
