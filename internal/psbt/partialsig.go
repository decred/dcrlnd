package psbt

import (
	"bytes"
)

// PartialSig encapsulate a (BTC public key, ECDSA signature)
// pair, note that the fields are stored as byte slices, not
// btcec.PublicKey or btcec.Signature (because manipulations will
// be with the former not the latter, here); compliance with consensus
// serialization is enforced with .checkValid()
type PartialSig struct {
	PubKey    []byte
	Signature []byte
}

// PartialSigSorter implements sort.Interface for PartialSig.
type PartialSigSorter []*PartialSig

func (s PartialSigSorter) Len() int { return len(s) }

func (s PartialSigSorter) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

func (s PartialSigSorter) Less(i, j int) bool {
	return bytes.Compare(s[i].PubKey, s[j].PubKey) < 0
}
