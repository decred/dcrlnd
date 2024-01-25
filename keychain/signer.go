package keychain

import (
	"fmt"

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

func (p *PubKeyMessageSigner) KeyLocator() KeyLocator {
	return p.keyLoc
}

func (p *PubKeyMessageSigner) SignMessage(message []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	return p.digestSigner.SignMessage(p.keyLoc, message, doubleHash)
}

func (p *PubKeyMessageSigner) SignMessageCompact(msg []byte,
	doubleHash bool) ([]byte, error) {

	return p.digestSigner.SignMessageCompact(p.keyLoc, msg, doubleHash)
}

func NewPrivKeyMessageSigner(privKey *secp256k1.PrivateKey,
	keyLoc KeyLocator) *PrivKeyMessageSigner {

	return &PrivKeyMessageSigner{
		privKey: privKey,
		keyLoc:  keyLoc,
	}
}

type PrivKeyMessageSigner struct {
	keyLoc  KeyLocator
	privKey *secp256k1.PrivateKey
}

func (p *PrivKeyMessageSigner) PubKey() *secp256k1.PublicKey {
	return p.privKey.PubKey()
}

func (p *PrivKeyMessageSigner) KeyLocator() KeyLocator {
	return p.keyLoc
}

func (p *PrivKeyMessageSigner) SignMessage(msg []byte,
	doubleHash bool) (*ecdsa.Signature, error) {

	var digest []byte
	if doubleHash {
		return nil, fmt.Errorf("dcrlnd does not do doubleHash signing")
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.Sign(p.privKey, digest), nil
}

func (p *PrivKeyMessageSigner) SignMessageCompact(msg []byte,
	doubleHash bool) ([]byte, error) {

	var digest []byte
	if doubleHash {
		return nil, fmt.Errorf("dcrlnd does not do doubleHash signing")
	} else {
		digest = chainhash.HashB(msg)
	}
	return ecdsa.SignCompact(p.privKey, digest[:], true), nil
}

var _ SingleKeyMessageSigner = (*PubKeyMessageSigner)(nil)
var _ SingleKeyMessageSigner = (*PrivKeyMessageSigner)(nil)
