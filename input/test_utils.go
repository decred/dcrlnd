package input

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/decred/dcrd/chaincfg/chainhash"
	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrec"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrd/txscript/v4"
	"github.com/decred/dcrd/txscript/v4/sign"
	"github.com/decred/dcrd/txscript/v4/stdaddr"
	"github.com/decred/dcrd/txscript/v4/stdscript"
	"github.com/decred/dcrd/wire"
)

var (
	// For simplicity a single priv key controls all of our test outputs.
	testWalletPrivKey = []byte{
		0x2b, 0xd8, 0x06, 0xc9, 0x7f, 0x0e, 0x00, 0xaf,
		0x1a, 0x1f, 0xc3, 0x32, 0x8f, 0xa7, 0x63, 0xa9,
		0x26, 0x97, 0x23, 0xc8, 0xdb, 0x8f, 0xac, 0x4f,
		0x93, 0xaf, 0x71, 0xdb, 0x18, 0x6d, 0x6e, 0x90,
	}

	// We're alice :)
	bobsPrivKey = []byte{
		0x81, 0xb6, 0x37, 0xd8, 0xfc, 0xd2, 0xc6, 0xda,
		0x63, 0x59, 0xe6, 0x96, 0x31, 0x13, 0xa1, 0x17,
		0xd, 0xe7, 0x95, 0xe4, 0xb7, 0x25, 0xb8, 0x4d,
		0x1e, 0xb, 0x4c, 0xfd, 0x9e, 0xc5, 0x8c, 0xe9,
	}

	// Use a hard-coded HD seed.
	testHdSeed = chainhash.Hash{
		0xb7, 0x94, 0x38, 0x5f, 0x2d, 0x1e, 0xf7, 0xab,
		0x4d, 0x92, 0x73, 0xd1, 0x90, 0x63, 0x81, 0xb4,
		0x4f, 0x2f, 0x6f, 0x25, 0x88, 0xa3, 0xef, 0xb9,
		0x6a, 0x49, 0x18, 0x83, 0x31, 0x98, 0x47, 0x53,
	}
)

// MockSigner is a simple implementation of the Signer interface. Each one has
// a set of private keys in a slice and can sign messages using the appropriate
// one.
type MockSigner struct {
	Privkeys  []*secp256k1.PrivateKey
	NetParams *chaincfg.Params
}

// SignOutputRaw generates a signature for the passed transaction according to
// the data within the passed SignDescriptor.
func (m *MockSigner) SignOutputRaw(tx *wire.MsgTx,
	signDesc *SignDescriptor) (Signature, error) {

	pubkey := signDesc.KeyDesc.PubKey
	switch {
	case signDesc.SingleTweak != nil:
		pubkey = TweakPubKeyWithTweak(pubkey, signDesc.SingleTweak)
	case signDesc.DoubleTweak != nil:
		pubkey = DeriveRevocationPubkey(pubkey, signDesc.DoubleTweak.PubKey())
	}

	hash160 := dcrutil.Hash160(pubkey.SerializeCompressed())
	privKey := m.findKey(hash160, signDesc.SingleTweak, signDesc.DoubleTweak)
	if privKey == nil {
		return nil, fmt.Errorf("mock signer does not have key")
	}

	sig, err := sign.RawTxInSignature(tx, signDesc.InputIndex,
		signDesc.WitnessScript, signDesc.HashType, privKey.Serialize(),
		dcrec.STEcdsaSecp256k1)
	if err != nil {
		return nil, err
	}

	return ecdsa.ParseDERSignature(sig[:len(sig)-1])
}

func (m *MockSigner) ComputeInputScript(tx *wire.MsgTx, signDesc *SignDescriptor) (*Script, error) {
	scriptType, addresses := stdscript.ExtractAddrs(
		signDesc.Output.Version, signDesc.Output.PkScript, m.NetParams)

	if len(addresses) != 1 {
		return nil, fmt.Errorf("unexpected nb of addresses (got %d, want %d)",
			len(addresses), 1)
	}

	switch scriptType {
	case stdscript.STPubKeyHashEcdsaSecp256k1:
		privKey := m.findKey(addresses[0].(stdaddr.Hash160er).Hash160()[:], signDesc.SingleTweak,
			signDesc.DoubleTweak)
		if privKey == nil {
			return nil, fmt.Errorf("mock signer does not have key for "+
				"address %v", addresses[0])
		}

		sigScript, err := sign.SignatureScript(
			tx, signDesc.InputIndex, signDesc.Output.PkScript,
			txscript.SigHashAll, privKey.Serialize(),
			dcrec.STEcdsaSecp256k1, true,
		)
		if err != nil {
			return nil, err
		}

		return &Script{SigScript: sigScript}, nil

	default:
		return nil, fmt.Errorf("unexpected script type: %v", scriptType)
	}
}

// findKey searches through all stored private keys and returns one
// corresponding to the hashed pubkey if it can be found. The public key may
// either correspond directly to the private key or to the private key with a
// tweak applied.
func (m *MockSigner) findKey(needleHash160 []byte, singleTweak []byte,
	doubleTweak *secp256k1.PrivateKey) *secp256k1.PrivateKey {

	for _, privkey := range m.Privkeys {
		// First check whether public key is directly derived from private key.
		hash160 := dcrutil.Hash160(privkey.PubKey().SerializeCompressed())
		if bytes.Equal(hash160, needleHash160) {
			return privkey
		}

		// Otherwise check if public key is derived from tweaked private key.
		switch {
		case singleTweak != nil:
			privkey = TweakPrivKey(privkey, singleTweak)
		case doubleTweak != nil:
			privkey = DeriveRevocationPrivKey(privkey, doubleTweak)
		default:
			continue
		}
		hash160 = dcrutil.Hash160(privkey.PubKey().SerializeCompressed())
		if bytes.Equal(hash160, needleHash160) {
			return privkey
		}
	}
	return nil
}

// pubkeyFromHex parses a Decred public key from a hex encoded string.
func pubkeyFromHex(keyHex string) (*secp256k1.PublicKey, error) {
	bytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	return secp256k1.ParsePubKey(bytes)
}

// privkeyFromHex parses a Decred private key from a hex encoded string.
func privkeyFromHex(keyHex string) (*secp256k1.PrivateKey, error) {
	bytes, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, err
	}
	key := secp256k1.PrivKeyFromBytes(bytes)
	return key, nil

}

// pubkeyToHex serializes a Decred public key to a hex encoded string.
func pubkeyToHex(key *secp256k1.PublicKey) string {
	return hex.EncodeToString(key.SerializeCompressed())
}

// privkeyFromHex serializes a Decred private key to a hex encoded string.
func privkeyToHex(key *secp256k1.PrivateKey) string {
	return hex.EncodeToString(key.Serialize())
}
