package wtmock

import (
	"sync"

	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/wire"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/keychain"
)

// MockSigner is an input.Signer that allows one to add arbitrary private keys
// and sign messages by passing the assigned keychain.KeyLocator.
type MockSigner struct {
	mu sync.Mutex

	index uint32
	keys  map[keychain.KeyLocator]*secp256k1.PrivateKey
}

// NewMockSigner returns a fresh MockSigner.
func NewMockSigner() *MockSigner {
	return &MockSigner{
		keys: make(map[keychain.KeyLocator]*secp256k1.PrivateKey),
	}
}

// SignOutputRaw signs an input on the passed transaction using the input index
// in the sign descriptor. The returned signature is the raw DER-encoded
// signature without the signhash flag.
func (s *MockSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (input.Signature, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	witnessScript := signDesc.WitnessScript

	privKey, ok := s.keys[signDesc.KeyDesc.KeyLocator]
	if !ok {
		panic("cannot sign w/ unknown key")
	}

	sig, err := sign.RawTxInSignature(
		tx, signDesc.InputIndex,
		witnessScript, signDesc.HashType, privKey.Serialize(),
		dcrec.STEcdsaSecp256k1,
	)
	if err != nil {
		return nil, err
	}

	return ecdsa.ParseDERSignature(sig[:len(sig)-1])
}

// ComputeInputScript is not implemented.
func (s *MockSigner) ComputeInputScript(tx *wire.MsgTx,
	signDesc *input.SignDescriptor) (*input.Script, error) {
	panic("not implemented")
}

// AddPrivKey records the passed privKey in the MockSigner's registry of keys it
// can sign with in the future. A unique key locator is returned, allowing the
// caller to sign with this key when presented via an input.SignDescriptor.
func (s *MockSigner) AddPrivKey(privKey *secp256k1.PrivateKey) keychain.KeyLocator {
	s.mu.Lock()
	defer s.mu.Unlock()

	keyLoc := keychain.KeyLocator{
		Index: s.index,
	}
	s.index++

	s.keys[keyLoc] = privKey

	return keyLoc
}

// Compile-time constraint ensuring the MockSigner implements the input.Signer
// interface.
var _ input.Signer = (*MockSigner)(nil)
