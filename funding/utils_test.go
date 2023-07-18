//go:build !rpctest
// +build !rpctest

package funding

import "github.com/decred/dcrd/dcrec/secp256k1/v4"

func privKeyFromBytes(b []byte) (*secp256k1.PrivateKey, *secp256k1.PublicKey) {
	k := secp256k1.PrivKeyFromBytes(b)
	return k, k.PubKey()
}

func modNScalar(b []byte) *secp256k1.ModNScalar {
	var m secp256k1.ModNScalar
	m.SetByteSlice(b)
	return &m
}
