package psbt

import (
	"bytes"
	"encoding/binary"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// Bip32Derivation encapsulates the data for the input and output
// Bip32Derivation key-value fields.
//
// TODO(roasbeef): use hdkeychain here instead?
type Bip32Derivation struct {
	// PubKey is the raw pubkey serialized in compressed format.
	PubKey []byte

	// MasterKeyFingerprint is the finger print of the master pubkey.
	MasterKeyFingerprint uint32

	// Bip32Path is the BIP 32 path with child index as a distinct integer.
	Bip32Path []uint32
}

// checkValid ensures that the PubKey in the Bip32Derivation struct is valid.
func (pb *Bip32Derivation) checkValid() bool {
	_, err := secp256k1.ParsePubKey(pb.PubKey)
	return err == nil
}

// Bip32Sorter implements sort.Interface for the Bip32Derivation struct.
type Bip32Sorter []*Bip32Derivation

func (s Bip32Sorter) Len() int { return len(s) }

func (s Bip32Sorter) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s Bip32Sorter) Less(i, j int) bool {
	return bytes.Compare(s[i].PubKey, s[j].PubKey) < 0
}

// SerializeBIP32Derivation takes a master key fingerprint as defined in BIP32,
// along with a path specified as a list of uint32 values, and returns a
// bytestring specifying the derivation in the format required by BIP174: //
// master key fingerprint (4) || child index (4) || child index (4) || ....
func SerializeBIP32Derivation(masterKeyFingerprint uint32,
	bip32Path []uint32) []byte {

	var masterKeyBytes [4]byte
	binary.LittleEndian.PutUint32(masterKeyBytes[:], masterKeyFingerprint)

	derivationPath := make([]byte, 0, 4+4*len(bip32Path))
	derivationPath = append(derivationPath, masterKeyBytes[:]...)
	for _, path := range bip32Path {
		var pathbytes [4]byte
		binary.LittleEndian.PutUint32(pathbytes[:], path)
		derivationPath = append(derivationPath, pathbytes[:]...)
	}

	return derivationPath
}
