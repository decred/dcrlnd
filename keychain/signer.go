package keychain

import (
	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

func NewPubKeyMessageSigner(pubKey *secp256k1.PublicKey, keyLoc KeyLocator,
	signer MessageSignerRing) *PubKeyMessageSigner {

	return &PubKeyMessageSigner{
		pubKey:       pubKey,
		keyLoc:       keyLoc,
		digestSigner: signer,
	}
}

type PubKeyMessageSigner struct {
	pubKey       *secp256k1.PublicKey
	keyLoc       KeyLocator
	digestSigner MessageSignerRing
}

func (p *PubKeyMessageSigner) PubKey() *secp256k1.PublicKey {
	return p.pubKey
}

func (p *PubKeyMessageSigner) SignMessage(message []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	return p.digestSigner.SignMessage(p.keyLoc, message, doubleHash)
}

func (p *PubKeyMessageSigner) SignMessageCompact(msg []byte,
	doubleHash bool) ([]byte, error) {

	return p.digestSigner.SignMessageCompact(p.keyLoc, msg, doubleHash)
}

type PrivKeyMessageSigner struct {
	PrivKey *secp256k1.PrivateKey
}

func (p *PrivKeyMessageSigner) PubKey() *secp256k1.PublicKey {
	return p.PrivKey.PubKey()
}

func (p *PrivKeyMessageSigner) SignMessage(msg []byte,
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

func (p *PrivKeyMessageSigner) SignMessageCompact(msg []byte,
	doubleHash bool) ([]byte, error) {

	var digest []byte
	if doubleHash {
		digest1 := chainhash.HashB(msg)
		digest = chainhash.HashB(digest1)
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.SignCompact(p.PrivKey, digest[:], true), nil
}

var _ SingleKeyMessageSigner = (*PubKeyMessageSigner)(nil)
var _ SingleKeyMessageSigner = (*PrivKeyMessageSigner)(nil)
