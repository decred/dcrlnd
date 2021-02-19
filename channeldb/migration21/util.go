package migration21

import (
	"encoding/hex"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

func mustDecodeHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func mustDecodeModNScalar(s string) *secp256k1.ModNScalar {
	b := mustDecodeHex(s)
	res := new(secp256k1.ModNScalar)
	if res.SetByteSlice(b) {
		panic("modNScalar overflowed")
	}
	return res
}
