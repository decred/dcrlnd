package keychain

import (
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func NewPubKeyDigestSigner(keyDesc KeyDescriptor,
	signer DigestSignerRing) *PubKeyDigestSigner {

	return &PubKeyDigestSigner{
		keyDesc:      keyDesc,
		digestSigner: signer,
	}
}

type PubKeyDigestSigner struct {
	keyDesc      KeyDescriptor
	digestSigner DigestSignerRing
}

func (p *PubKeyDigestSigner) PubKey() *secp256k1.PublicKey {
	return p.keyDesc.PubKey
}

func (p *PubKeyDigestSigner) SignMessage(message []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	return p.digestSigner.SignMessage(p.keyDesc, message, doubleHash)
}

func (p *PubKeyDigestSigner) SignDigestCompact(digest [32]byte) ([]byte,
	error) {

	return p.digestSigner.SignDigestCompact(p.keyDesc, digest)
}

type PrivKeyDigestSigner struct {
	PrivKey *secp256k1.PrivateKey
}

func (p *PrivKeyDigestSigner) PubKey() *secp256k1.PublicKey {
	return p.PrivKey.PubKey()
}

func (p *PrivKeyDigestSigner) SignMessage(msg []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	var digest []byte
	if doubleHash {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.Sign(p.PrivKey, digest), nil
}

func (p *PrivKeyDigestSigner) SignDigestCompact(digest [32]byte) ([]byte,
	error) {

	return ecdsa.SignCompact(p.PrivKey, digest[:], true), nil
}

var _ SingleKeyDigestSigner = (*PubKeyDigestSigner)(nil)
var _ SingleKeyDigestSigner = (*PrivKeyDigestSigner)(nil)
