package mock

import (
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/wire"

	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
)

// DummySignature is a dummy Signature implementation.
type DummySignature struct{}

// Serialize returns an empty byte slice.
func (d *DummySignature) Serialize() []byte {
	return []byte{}
}

// Verify always returns true.
func (d *DummySignature) Verify(_ []byte, _ *secp256k1.PublicKey) bool {
	return true
}

// DummySigner is an implementation of the Signer interface that returns
// dummy values when called.
type DummySigner struct{}

// SignOutputRaw returns a dummy signature.
func (d *DummySigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (input.Signature, error) {

	return &DummySignature{}, nil
}

// ComputeInputScript returns nil for both values.
func (d *DummySigner) ComputeInputScript(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (*input.Script, error) {

	return &input.Script{}, nil
}

// SingleSigner is an implementation of the Signer interface that signs
// everything with a single private key.
type SingleSigner struct {
	Privkey *secp256k1.PrivateKey
	KeyLoc  keychain.KeyLocator
}

// SignOutputRaw generates a signature for the passed transaction using the
// stored private key.
func (s *SingleSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (input.Signature, error) {

	witnessScript := signDesc.WitnessScript
	privKey := s.Privkey

	if !privKey.PubKey().IsEqual(signDesc.KeyDesc.PubKey) {
		return nil, fmt.Errorf("incorrect key passed")
	}

	switch {
	case signDesc.SingleTweak != nil:
		privKey = input.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = input.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)
	}

	sig, err := sign.RawTxInSignature(tx,
		signDesc.InputIndex, witnessScript, signDesc.HashType,
		privKey.Serialize(), dcrec.STEcdsaSecp256k1)
	if err != nil {
		return nil, err
	}

	return ecdsa.ParseDERSignature(sig[:len(sig)-1])
}

// ComputeInputScript computes an input script with the stored private key
// given a transaction and a SignDescriptor.
func (s *SingleSigner) ComputeInputScript(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (*input.Script, error) {

	privKey := s.Privkey

	switch {
	case signDesc.SingleTweak != nil:
		privKey = input.TweakPrivKey(privKey,
			signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		privKey = input.DeriveRevocationPrivKey(privKey,
			signDesc.DoubleTweak)
	}

	witnessScript, err := sign.SignatureScript(tx,
		signDesc.InputIndex, signDesc.Output.PkScript,
		signDesc.HashType, privKey.Serialize(), dcrec.STEcdsaSecp256k1, true)
	if err != nil {
		return nil, err
	}

	return &input.Script{
		SigScript: witnessScript,
	}, nil
}

// SignMessage takes a public key and a message and only signs the message
// with the stored private key if the public key matches the private key.
func (s *SingleSigner) SignMessage(keyLoc keychain.KeyLocator,
	msg []byte) (*ecdsa.Signature, error) {

	mockKeyLoc := s.KeyLoc
	if s.KeyLoc.IsEmpty() {
		mockKeyLoc = keyLoc
	}

	if keyLoc != mockKeyLoc {
		return nil, fmt.Errorf("unknown public key")
	}

	digest := chainhash.HashB(msg)
	sign := ecdsa.Sign(s.Privkey, digest)
	return sign, nil
}
